package api

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/bng147/gonas/internal/backup"
	"github.com/bng147/gonas/internal/monitor"
	"github.com/bng147/gonas/internal/state"
)

// backupFailureMessage 組出備份失敗通知的內文(給 email/webhook/log 看)。
func backupFailureMessage(job backup.Job, result backup.RunResult) string {
	errText := result.Error
	if errText == "" {
		errText = "(no error detail)"
	}
	return fmt.Sprintf(
		"GoNAS 排程備份失敗 / scheduled backup failed\n\n"+
			"工作 Job:    %s\n"+
			"來源 Source: %s\n"+
			"目的 Dest:   %s\n"+
			"時間 Time:   %s\n"+
			"錯誤 Error:  %s\n",
		job.Name, job.SourcePath, job.DestPath,
		result.FinishedAt.Format(time.RFC1123Z), errText,
	)
}

// startBackupScheduler 幫一個已經啟用的備份工作建立並啟動排程,登記進
// s.backupSchedulers 讓之後刪除/daemon 關閉時找得到、停得掉。呼叫端
// (New()、handleBackupJobsCreate)負責只在 job.Enabled 為 true 時呼叫。
func (s *Server) startBackupScheduler(job backup.Job) {
	sched := backup.NewJobScheduler(s.logger)
	sched.Start(context.Background(), job.Schedule, func(ctx context.Context) {
		s.runBackupJob(ctx, job.ID)
	})

	s.backupMu.Lock()
	s.backupSchedulers[job.ID] = sched
	s.backupMu.Unlock()
}

// stopBackupScheduler 停掉並移除一個備份工作的排程(如果有在跑的話)。
// 刪除從來沒啟用過的工作時這裡是安全的 no-op。
func (s *Server) stopBackupScheduler(id string) {
	s.backupMu.Lock()
	sched, ok := s.backupSchedulers[id]
	delete(s.backupSchedulers, id)
	s.backupMu.Unlock()

	if ok {
		sched.Stop()
	}
}

// runBackupJob 執行一次備份並把結果寫回 state。獨立成一個方法讓排程
// (startBackupScheduler)跟手動觸發(handleBackupJobsRun)共用同一段
// 「執行 + 記錄結果」邏輯,不會有兩條路徑對 LastRun 的更新方式不一致的
// 風險。執行當下重新從 store 讀一次 job 設定(而不是用建立排程當下的
// 舊副本),確保萬一之後有其他地方能修改 job 設定,排程用的一定是最新的。
func (s *Server) runBackupJob(ctx context.Context, id string) {
	var job *backup.Job
	for _, j := range s.store.Snapshot().BackupJobs {
		if j.ID == id {
			jc := j
			job = &jc
			break
		}
	}
	if job == nil {
		s.logger.Warn("scheduled backup fired for a job that no longer exists", "jobId", id)
		return
	}

	s.logger.Info("running backup job", "jobId", job.ID, "name", job.Name)
	result := backup.RunBackup(ctx, s.runner, *job, time.Now())
	if result.Success {
		s.logger.Info("backup job finished", "jobId", job.ID, "name", job.Name, "snapshot", result.SnapshotDir)
	} else {
		s.logger.Error("backup job failed", "jobId", job.ID, "name", job.Name, "err", result.Error)
		// 第三十二輪:備份失敗不再只寫 log + 更新 LastRun(那要打開網頁
		// 才看得到)。主動透過跟告警同一組的通知管道(webhook/email/log)
		// 送一則「備份失敗」通知,讓「以為在備份、其實已連續失敗好幾週」
		// 這種靜默失敗被看見。走 best-effort:通知本身失敗只記 log,不影響
		// 下面把結果寫回 state。用獨立的 context(不綁這次備份的 ctx)——
		// 備份可能是因為 ctx 被取消而失敗的,通知不該跟著一起被取消。
		ev := monitor.Event{
			Kind:    monitor.EventKindBackupFailed,
			At:      time.Now(),
			Subject: "備份失敗 Backup failed: " + job.Name,
			Message: backupFailureMessage(*job, result),
		}
		if err := s.buildNotifier().Notify(context.Background(), ev); err != nil {
			s.logger.Warn("sending backup-failure notification failed", "jobId", job.ID, "err", err)
		}
	}

	if err := s.store.Update(func(st *state.State) error {
		for i := range st.BackupJobs {
			if st.BackupJobs[i].ID == id {
				r := result
				st.BackupJobs[i].LastRun = &r
				break
			}
		}
		return nil
	}); err != nil {
		s.logger.Error("persisting backup job result failed", "jobId", job.ID, "err", err)
	}
}

func (s *Server) handleBackupJobsList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.store.Snapshot().BackupJobs)
}

type createBackupJobRequest struct {
	Name           string          `json:"name"`
	SourcePath     string          `json:"sourcePath"`
	DestPath       string          `json:"destPath"`
	RetentionCount int             `json:"retentionCount"`
	Enabled        bool            `json:"enabled"`
	Schedule       backup.Schedule `json:"schedule"`
}

// handleBackupJobsCreate 驗證並儲存一份新的備份工作設定。驗證失敗(例如
// 目的地路徑落在來源路徑底下)一律回 400,不會讓一份會造成遞迴複製的
// 設定寫進 state.json —— 見 backup.Job.Validate 的說明。
func (s *Server) handleBackupJobsCreate(w http.ResponseWriter, r *http.Request) {
	var req createBackupJobRequest
	if !readJSON(w, r, &req) {
		return
	}

	job := backup.Job{
		ID:             newID(),
		Name:           req.Name,
		SourcePath:     req.SourcePath,
		DestPath:       req.DestPath,
		RetentionCount: req.RetentionCount,
		Enabled:        req.Enabled,
		Schedule:       req.Schedule,
	}
	if err := job.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	if err := s.store.Update(func(st *state.State) error {
		st.BackupJobs = append(st.BackupJobs, job)
		return nil
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	if job.Enabled {
		s.startBackupScheduler(job)
	}

	writeJSON(w, http.StatusCreated, job)
}

func (s *Server) handleBackupJobsDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	found := false
	if err := s.store.Update(func(st *state.State) error {
		kept := st.BackupJobs[:0]
		for _, j := range st.BackupJobs {
			if j.ID == id {
				found = true
				continue
			}
			kept = append(kept, j)
		}
		st.BackupJobs = kept
		return nil
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, errBackupJobNotFound)
		return
	}

	s.stopBackupScheduler(id)
	w.WriteHeader(http.StatusNoContent)
}

type runBackupJobResponse struct {
	Message string `json:"message"`
}

// handleBackupJobsRun 立刻觸發一次備份,不等它跑完就回應 ——
// 備份可能牽涉到大量資料、跑好幾分鐘甚至更久,讓 HTTP 請求等在那裡
// 不是合理的使用者體驗。執行結果(成功與否、快照路徑)會反映在下一次
// GET /api/v1/backup/jobs 回應的 lastRun 欄位裡。
func (s *Server) handleBackupJobsRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	exists := false
	for _, j := range s.store.Snapshot().BackupJobs {
		if j.ID == id {
			exists = true
			break
		}
	}
	if !exists {
		writeError(w, http.StatusNotFound, errBackupJobNotFound)
		return
	}

	go s.runBackupJob(context.Background(), id)

	writeJSON(w, http.StatusAccepted, runBackupJobResponse{Message: "備份已開始在背景執行,完成後請重新整理查看結果。"})
}

// restoreRequest 是 POST .../restore 的請求 body。
type restoreRequest struct {
	Snapshot   string `json:"snapshot"`   // ListSnapshots 回的 name(時間戳記目錄名)
	TargetPath string `json:"targetPath"` // 還原到哪(絕對路徑);留空則還原回原本的來源目錄
}

// handleBackupJobsRestore 把某一份快照還原到指定目錄(第六十輪產品覆核 P0:
// 補上「還原」——備份能建能列卻不能還原等於白備份)。同步執行 rsync 並回傳
// 結果,讓使用者馬上知道成功與否;還原可能很久,所以關掉這條請求的讀寫逾時
// (跟檔案上傳一樣),並用脫鉤的 context 讓使用者關掉瀏覽器也不會把還原砍在
// 半路。requireAdmin。
func (s *Server) handleBackupJobsRestore(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var job *backup.Job
	for _, j := range s.store.Snapshot().BackupJobs {
		if j.ID == id {
			jc := j
			job = &jc
			break
		}
	}
	if job == nil {
		writeError(w, http.StatusNotFound, errBackupJobNotFound)
		return
	}

	var req restoreRequest
	if !readJSON(w, r, &req) {
		return
	}
	target := req.TargetPath
	if target == "" {
		target = job.SourcePath // 預設還原回原本的來源目錄
	}

	if rc := http.NewResponseController(w); rc != nil {
		_ = rc.SetReadDeadline(time.Time{})
		_ = rc.SetWriteDeadline(time.Time{})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 24*time.Hour)
	defer cancel()

	s.logger.Info("restoring backup snapshot", "job", id, "snapshot", req.Snapshot, "target", target)
	result := backup.RunRestore(ctx, s.runner, *job, req.Snapshot, target)
	if !result.Success {
		s.logger.Error("backup restore failed", "job", id, "err", result.Error)
		writeError(w, http.StatusInternalServerError, fmt.Errorf("%s", result.Error))
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleBackupJobsSnapshots(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var job *backup.Job
	for _, j := range s.store.Snapshot().BackupJobs {
		if j.ID == id {
			jc := j
			job = &jc
			break
		}
	}
	if job == nil {
		writeError(w, http.StatusNotFound, errBackupJobNotFound)
		return
	}

	snapshots, err := backup.ListSnapshots(*job)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if snapshots == nil {
		snapshots = []backup.SnapshotInfo{}
	}
	writeJSON(w, http.StatusOK, snapshots)
}
