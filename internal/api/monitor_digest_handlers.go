package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/bng147/gonas/internal/backup"
	"github.com/bng147/gonas/internal/cron"
	"github.com/bng147/gonas/internal/monitor"
	"github.com/bng147/gonas/internal/state"
)

// digestResponse 是 GET/PUT /api/v1/monitor/digest 共用的回應形狀,直接
// 對應 state.DigestConfig,只是把 *time.Time 轉成字串——JSON 裡的
// null vs. 缺欄位在前端處理起來比「有沒有值的 *time.Time」更直覺,跟
// 這個專案其他「上次執行時間」欄位(例如 backup.RunResult)的表示法
// 一致。
type digestResponse struct {
	Enabled          bool     `json:"enabled"`
	CronExpr         string   `json:"cronExpr,omitempty"`
	NotifierIDs      []string `json:"notifierIds"`
	EmailNotifierIDs []string `json:"emailNotifierIds"`
	LastSentAt       string   `json:"lastSentAt,omitempty"`
}

func buildDigestResponse(cfg state.DigestConfig) digestResponse {
	resp := digestResponse{
		Enabled:          cfg.Enabled,
		CronExpr:         cfg.CronExpr,
		NotifierIDs:      cfg.NotifierIDs,
		EmailNotifierIDs: cfg.EmailNotifierIDs,
	}
	if resp.NotifierIDs == nil {
		resp.NotifierIDs = []string{}
	}
	if resp.EmailNotifierIDs == nil {
		resp.EmailNotifierIDs = []string{}
	}
	if cfg.LastSentAt != nil {
		resp.LastSentAt = cfg.LastSentAt.UTC().Format(time.RFC3339)
	}
	return resp
}

// handleMonitorDigestGet 回傳目前的健康摘要設定跟上次送出時間。跟
// 「查詢目前狀態」一貫的 requireAuth(不用 requireAdmin)分法一樣——
// RoleViewer 也該能看到有沒有設定這個功能,只有變更設定/立即送出才需要
// RoleAdmin。
func (s *Server) handleMonitorDigestGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, buildDigestResponse(s.store.Snapshot().Digest))
}

// digestSettingsRequest 是 PUT /api/v1/monitor/digest 的請求 body。
type digestSettingsRequest struct {
	Enabled          bool     `json:"enabled"`
	CronExpr         string   `json:"cronExpr"`
	NotifierIDs      []string `json:"notifierIds"`
	EmailNotifierIDs []string `json:"emailNotifierIds"`
}

// handleMonitorDigestSet 更新健康摘要設定,驗證通過就寫回 store,並且
// 立刻重新套用背景排程(停掉舊的、視新設定決定要不要啟動一個新的)——
// 跟 HTTPS 設定不同,不需要重啟 gonasd 才生效,理由跟
// selfupdate.Checker 的背景檢查器一樣:排程本身是可以在執行期間安全
// 換掉的背景 goroutine,沒有必要為了「改個 cron 表達式」要求使用者
// 重新啟動整個 daemon。
func (s *Server) handleMonitorDigestSet(w http.ResponseWriter, r *http.Request) {
	var req digestSettingsRequest
	if !readJSON(w, r, &req) {
		return
	}

	cfg := state.DigestConfig{
		Enabled:          req.Enabled,
		CronExpr:         strings.TrimSpace(req.CronExpr),
		NotifierIDs:      req.NotifierIDs,
		EmailNotifierIDs: req.EmailNotifierIDs,
	}
	if err := cfg.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	if err := s.store.Update(func(st *state.State) error {
		// LastSentAt 是背景排程/立即送出自己維護的狀態,不是這支「設定」
		// 端點的使用者可以直接覆寫的欄位——保留舊值,只更新使用者實際
		// 填寫的那幾個欄位。
		cfg.LastSentAt = st.Digest.LastSentAt
		st.Digest = cfg
		return nil
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	s.restartDigestScheduler(cfg)
	writeJSON(w, http.StatusOK, buildDigestResponse(cfg))
}

type digestSendResponse struct {
	Message string `json:"message"`
}

// handleMonitorDigestSend 立刻送出一次健康摘要,不等待排程的下一個
// 週期——跟 handleSystemUpdateApply/handleBackupJobsRun 一樣的「立刻回
// 202、背景執行」模式,因為送出動作(尤其是 SMTP)可能要幾秒鐘,不值得
// 讓 HTTP 請求同步等在那裡。刻意允許在完全沒有設定 CronExpr/Enabled 的
// 情況下也能呼叫——「立即測試一次摘要長什麼樣子、有沒有送達」是使用者
// 在正式啟用排程之前很自然會想做的事,不該被「你要先啟用排程」這個
// 前提擋住。
func (s *Server) handleMonitorDigestSend(w http.ResponseWriter, r *http.Request) {
	go s.runDigestSend(context.Background())
	writeJSON(w, http.StatusAccepted, digestSendResponse{
		Message: "健康摘要已開始在背景送出,完成後可以在通知管道(log/webhook/email)確認是否收到。",
	})
}

// startDigestScheduler 啟動(或重新啟動)背景排程 goroutine。呼叫端
// 保證 cronExpr 已經通過 state.DigestConfig.Validate() 的檢查,理論上
// internal/cron.Parse 不會再失敗,但 DigestScheduler.Start 本身仍然
// 防禦性地處理 nextFn 出錯的情況(見該方法的說明),這裡不用重複擋。
func (s *Server) startDigestScheduler(cronExpr string) {
	s.digestMu.Lock()
	defer s.digestMu.Unlock()

	if s.digestScheduler != nil {
		s.digestScheduler.Stop()
	}
	s.digestScheduler = monitor.NewDigestScheduler(s.logger)
	nextFn := func(after time.Time) (time.Time, error) {
		sched, err := cron.Parse(cronExpr)
		if err != nil {
			return time.Time{}, err
		}
		return sched.Next(after)
	}
	s.digestScheduler.Start(context.Background(), nextFn, func(ctx context.Context) {
		s.runDigestSend(ctx)
	})
}

// stopDigestScheduler 停掉目前的背景排程(如果有的話),給 Server.Close
// 跟「使用者停用/清空設定」共用。
func (s *Server) stopDigestScheduler() {
	s.digestMu.Lock()
	defer s.digestMu.Unlock()

	if s.digestScheduler != nil {
		s.digestScheduler.Stop()
		s.digestScheduler = nil
	}
}

// restartDigestScheduler 依照最新設定決定要不要有一個在跑的背景排程——
// 停用、或啟用了但 CronExpr 是空字串(理論上不會發生,因為 Validate
// 已經擋掉這個組合,防禦性地一併處理)都停掉排程;否則重新啟動一個用
// 新表達式運作的排程,取代舊的。
func (s *Server) restartDigestScheduler(cfg state.DigestConfig) {
	if !cfg.Enabled || cfg.CronExpr == "" {
		s.stopDigestScheduler()
		return
	}
	s.startDigestScheduler(cfg.CronExpr)
}

// runDigestSend 組出目前的健康摘要內容,送給設定裡選取的 webhook/email
// 通知管道,成功之後更新 LastSentAt。跟 rebuildNotifier(給告警用)不同
// 的地方:告警一律包含保底的 LogNotifier,digest 也一樣寫一筆 log(方便
// 在完全沒設定任何 webhook/email 的情況下,至少能在 log 裡確認排程確實
// 有在跑),但只有使用者在 NotifierIDs/EmailNotifierIDs 裡明確選取、而且
// 那個管道本身也是 Enabled 的,才會真的收到 webhook/email——沒有「新增
// 一個告警用的 webhook 就自動也收到 digest」這種隱性行為。
func (s *Server) runDigestSend(ctx context.Context) {
	snap := s.store.Snapshot()

	sysSnap, ok := s.monitorHistory.Latest()
	if !ok {
		var err error
		sysSnap, err = s.monitorCollector.Sample()
		if err != nil {
			s.logger.Error("monitor: digest could not sample system resources", "err", err)
			return
		}
	}

	firing := s.alertEngine.Firing()
	var firingNames []string
	for _, rule := range snap.AlertRules {
		if firing[rule.ID] {
			firingNames = append(firingNames, rule.Name)
		}
	}

	summaries := make([]string, 0, len(snap.BackupJobs))
	for _, job := range snap.BackupJobs {
		summaries = append(summaries, summarizeBackupJob(job))
	}

	ev := monitor.BuildDigestEvent(monitor.DigestInput{
		Snapshot:        sysSnap,
		FiringRuleNames: firingNames,
		BackupSummaries: summaries,
	})

	notifiers := []monitor.Notifier{monitor.NewLogNotifier(s.logger)}
	for _, cfg := range snap.Notifiers {
		if cfg.Enabled && stringSliceContains(snap.Digest.NotifierIDs, cfg.ID) {
			notifiers = append(notifiers, monitor.NewWebhookNotifier(cfg, nil))
		}
	}
	for _, cfg := range snap.EmailNotifiers {
		if cfg.Enabled && stringSliceContains(snap.Digest.EmailNotifierIDs, cfg.ID) {
			notifiers = append(notifiers, monitor.NewEmailNotifier(cfg, nil))
		}
	}

	if err := (monitor.MultiNotifier{Notifiers: notifiers}).Notify(ctx, ev); err != nil {
		s.logger.Error("monitor: sending health digest failed", "err", err)
		return
	}

	now := time.Now()
	if err := s.store.Update(func(st *state.State) error {
		st.Digest.LastSentAt = &now
		return nil
	}); err != nil {
		s.logger.Error("monitor: recording digest lastSentAt failed", "err", err)
	}
}

// summarizeBackupJob 組出一句話描述一個備份工作最近一次執行的結果,給
// digest 內文用。刻意放在 internal/api 而不是 internal/backup——這是
// 「給人看的一段摘要文字」,屬於呈現層,跟 backup.Job/backup.RunResult
// 本身的資料結構是兩件事,類似 internal/monitor 現有的
// Schedule.Describe() 只是那個是給 Web UI 用、這裡是給 digest 純文字
// 內容用,格式需求不同,不需要勉強共用。
func summarizeBackupJob(job backup.Job) string {
	if job.LastRun == nil {
		return fmt.Sprintf("%s:還沒有執行過。", job.Name)
	}
	if job.LastRun.Success {
		return fmt.Sprintf("%s:上次成功於 %s。", job.Name, job.LastRun.FinishedAt.Format("2006-01-02 15:04"))
	}
	return fmt.Sprintf("%s:上次執行失敗(%s)——%s。", job.Name, job.LastRun.FinishedAt.Format("2006-01-02 15:04"), job.LastRun.Error)
}

func stringSliceContains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
