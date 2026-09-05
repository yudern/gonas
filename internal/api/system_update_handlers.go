package api

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/bng147/gonas/internal/selfupdate"
	"github.com/bng147/gonas/internal/state"
	"github.com/bng147/gonas/internal/version"
)

// applyUpdateStage 描述 self-update「套用」動作目前所在的階段,純粹是
// 給 GET /api/v1/system/update 顯示進度用的資訊,不影響實際行為。
type applyUpdateStage string

const (
	applyStageIdle        applyUpdateStage = ""
	applyStageFetching    applyUpdateStage = "fetching_manifest"
	applyStageDownloading applyUpdateStage = "downloading"
	applyStageApplying    applyUpdateStage = "applying"
	applyStageRestarting  applyUpdateStage = "restarting"
	applyStageFailed      applyUpdateStage = "failed"
)

// applyUpdateStatus 是 handleSystemUpdateApply 觸發的背景套用動作目前
// 的狀態,由 Server.applyMu 保護。成功走到重啟那一步之後不會再有機會被
// 讀到(程序馬上被 syscall.Exec 換掉),所以這裡真正有意義的是「還在
// 跑」跟「失敗了、原因是什麼」這兩種狀態。
type applyUpdateStatus struct {
	Stage     applyUpdateStage
	StartedAt time.Time
	Err       error
}

// systemUpdateResponse 是 GET /api/v1/system/update 的回應,合併三種
// 來源的資訊:目前執行中的版本(internal/version)、使用者設定的更新來源
// (state.State.Update)、背景檢查器最近一次的檢查結果
// (selfupdate.Checker.Latest),以及(如果有的話)目前套用動作的進度。
type systemUpdateResponse struct {
	CurrentVersion string `json:"currentVersion"`
	ManifestURL    string `json:"manifestUrl,omitempty"`
	Configured     bool   `json:"configured"`

	CheckedAt       string `json:"checkedAt,omitempty"`
	LatestVersion   string `json:"latestVersion,omitempty"`
	UpdateAvailable bool   `json:"updateAvailable"`
	Notes           string `json:"notes,omitempty"`
	CheckError      string `json:"checkError,omitempty"`

	ApplyInProgress bool   `json:"applyInProgress"`
	ApplyStage      string `json:"applyStage,omitempty"`
	ApplyError      string `json:"applyError,omitempty"`

	// BackupAvailable 代表執行檔旁邊有沒有一份 ".previous" 備份可以
	// 復原——Web UI 只在這是 true 的時候才顯示「復原到上一個版本」的
	// 按鈕,見 handleSystemUpdateRollback。
	BackupAvailable bool `json:"backupAvailable"`
}

// resolveExecPath 找出目前這個 gonasd 程序實際是從哪個檔案載入的,是
// runApplyUpdate、handleSystemUpdateRollback、buildSystemUpdateResponse
// (檢查有沒有備份可以復原)三個地方共用的邏輯,原本只寫在
// runApplyUpdate 裡面,這裡抽出來避免重複也避免三個地方對「怎麼解析
// 執行檔路徑」的做法慢慢分岔。
//
// 用 os.Executable() 而不是 os.Args[0]:後者在使用者透過相對路徑或
// PATH 查找啟動程式的情況下不一定是可靠的絕對路徑,os.Executable()
// 才是「目前這個程序實際是從哪個檔案載入的」的可靠來源,也是
// ApplyUpdate/Reexec/RollbackToBackup 都要求呼叫端提供的那個路徑。
// EvalSymlinks 額外解掉可能存在的符號連結(例如 /usr/local/bin/gonasd
// 之類的安裝路徑理論上可能是連結),確保置換的是「真正的執行檔本體
// 所在位置」,而不是連結本身(置換連結不會改變它指向的舊檔案)。
//
// s.updateExecPathFunc 預設是 nil,這裡才 fallback 成 os.Executable;
// 測試會把它換成回傳假執行檔路徑,見該欄位的說明,目的是避免測試不小心
// 覆寫掉正在跑測試的那個真正執行檔。
func (s *Server) resolveExecPath() (string, error) {
	resolveExecPath := s.updateExecPathFunc
	if resolveExecPath == nil {
		resolveExecPath = os.Executable
	}
	execPath, err := resolveExecPath()
	if err != nil {
		return "", fmt.Errorf("selfupdate: 找不到自己的執行檔路徑: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(execPath); err == nil {
		execPath = resolved
	}
	return execPath, nil
}

// buildSystemUpdateResponse 組出目前完整的自我更新狀態,GET 端點跟
// PUT/POST 端點成功之後都回傳同一份,讓前端不用另外再打一次 GET 就能
// 立刻反映最新狀態。
func (s *Server) buildSystemUpdateResponse() systemUpdateResponse {
	cfg := s.store.Snapshot().Update
	result := s.updateChecker.Latest()

	resp := systemUpdateResponse{
		CurrentVersion:  version.Version,
		ManifestURL:     cfg.ManifestURL,
		Configured:      cfg.ManifestURL != "",
		UpdateAvailable: result.UpdateAvailable,
		LatestVersion:   result.LatestVersion,
		Notes:           result.Notes,
	}
	if !result.CheckedAt.IsZero() {
		resp.CheckedAt = result.CheckedAt.UTC().Format(time.RFC3339)
	}
	if result.Err != nil {
		resp.CheckError = result.Err.Error()
	}

	s.applyMu.Lock()
	applyStatus := s.applyStatus
	s.applyMu.Unlock()
	if applyStatus.Stage != applyStageIdle {
		resp.ApplyStage = string(applyStatus.Stage)
		resp.ApplyInProgress = applyStatus.Stage != applyStageFailed
		if applyStatus.Err != nil {
			resp.ApplyError = applyStatus.Err.Error()
		}
	}

	// 檢查備份是不是存在,決定要不要在 Web UI 顯示「復原到上一個版本」
	// 按鈕。這裡刻意忽略 resolveExecPath 的錯誤——如果連執行檔路徑都
	// 解析不出來,就直接當作沒有備份可用(BackupAvailable 維持零值
	// false),不需要把這個內部錯誤也塞進這支通用的狀態回應裡。
	if execPath, err := s.resolveExecPath(); err == nil {
		if _, statErr := os.Stat(execPath + ".previous"); statErr == nil {
			resp.BackupAvailable = true
		}
	}

	return resp
}

// handleSystemUpdateGet 回傳目前的版本、更新來源設定,以及背景檢查器
// 最近一次的檢查結果。刻意只用 requireAuth(不用 requireAdmin)——「目前
// 是不是最新版本」是 RoleViewer 也該看得到的資訊,真正動到系統的設定/
// 檢查/套用才需要 RoleAdmin,見 router.go 路由註冊旁的說明。
func (s *Server) handleSystemUpdateGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.buildSystemUpdateResponse())
}

type systemUpdateSettingsRequest struct {
	ManifestURL string `json:"manifestUrl"`
}

// handleSystemUpdateSettingsSet 設定(或清空)更新來源網址。清空
// (ManifestURL 存成空字串)是刻意支援的操作,不是邊界情況——使用者
// 隨時可以把自我更新檢查整個關掉,回到「完全不對外發任何更新相關的網路
// 請求」的預設狀態,見 internal/selfupdate 套件註解的隱私設計說明。
func (s *Server) handleSystemUpdateSettingsSet(w http.ResponseWriter, r *http.Request) {
	var req systemUpdateSettingsRequest
	if !readJSON(w, r, &req) {
		return
	}

	manifestURL := strings.TrimSpace(req.ManifestURL)
	if err := s.store.Update(func(st *state.State) error {
		st.Update.ManifestURL = manifestURL
		return nil
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	writeJSON(w, http.StatusOK, s.buildSystemUpdateResponse())
}

// handleSystemUpdateCheck 立刻對目前設定的更新來源執行一次檢查,不等待
// 背景 ticker 的下一個週期(那個週期預設是 updateCheckInterval,對「使用者
// 剛設定好網址、想馬上看到結果」這種互動情境來說太久了)。
func (s *Server) handleSystemUpdateCheck(w http.ResponseWriter, r *http.Request) {
	manifestURL := s.store.Snapshot().Update.ManifestURL
	if manifestURL == "" {
		writeError(w, http.StatusBadRequest, errUpdateNotConfigured)
		return
	}

	s.updateChecker.CheckNow(r.Context(), version.Version, manifestURL, s.updateHTTPClient)
	writeJSON(w, http.StatusOK, s.buildSystemUpdateResponse())
}

type systemUpdateApplyResponse struct {
	Message string `json:"message"`
}

// handleSystemUpdateApply 是四支端點裡唯一真的會動到磁碟上執行檔的
// 一支,也是整個自我更新功能裡最敏感的動作——下載、驗證、置換執行檔、
// 重啟整個 gonasd 程序可能要幾十秒(取決於執行檔大小跟網路速度),讓
// HTTP 請求同步等在那裡不是合理的使用者體驗,所以跟
// handleBackupJobsRun 一樣採用「立刻回 202、背景執行、結果反映在下一次
// GET 回應」的模式——差別在於「結果」在成功的情況下其實是「程序被
// syscall.Exec 換掉,舊的這個 HTTP handler 根本沒機會被問到」,只有
// 失敗的情況才會留下可以查詢的錯誤訊息(見 applyUpdateStatus)。
func (s *Server) handleSystemUpdateApply(w http.ResponseWriter, r *http.Request) {
	manifestURL := s.store.Snapshot().Update.ManifestURL
	if manifestURL == "" {
		writeError(w, http.StatusBadRequest, errUpdateNotConfigured)
		return
	}

	s.applyMu.Lock()
	if s.applyStatus.Stage != applyStageIdle && s.applyStatus.Stage != applyStageFailed {
		s.applyMu.Unlock()
		writeError(w, http.StatusConflict, errUpdateAlreadyInProgress)
		return
	}
	s.applyStatus = applyUpdateStatus{Stage: applyStageFetching, StartedAt: time.Now()}
	s.applyMu.Unlock()

	// 用 context.Background() 而不是 r.Context():這個下載/驗證/置換的
	// 過程要花的時間可能遠超過這支 HTTP 請求本身的生命週期(請求已經在
	// 下面回了 202、連線隨時可能被使用者關掉),不該因為使用者關掉瀏覽器
	// 分頁就讓正在進行中的更新半途而廢。
	go s.runApplyUpdate(context.Background(), manifestURL)

	writeJSON(w, http.StatusAccepted, systemUpdateApplyResponse{
		Message: "更新已開始在背景下載與套用,完成後 gonasd 會自動重新啟動,請稍後重新整理頁面。",
	})
}

// setApplyStage 只更新階段欄位,保留原本的 StartedAt——單純是給
// runApplyUpdate 在往下推進每個步驟時回報進度用。
func (s *Server) setApplyStage(stage applyUpdateStage) {
	s.applyMu.Lock()
	s.applyStatus.Stage = stage
	s.applyMu.Unlock()
}

// failApply 把套用動作標記成失敗並記下原因,讓下一次 GET
// /api/v1/system/update 能把錯誤原因回報給使用者——self-update 失敗
// 不該是「靜默地什麼事都沒發生」,尤其這是使用者主動觸發、會盯著畫面
// 等結果的動作。
func (s *Server) failApply(err error) {
	s.logger.Error("applying self-update failed", "err", err)
	s.applyMu.Lock()
	s.applyStatus.Stage = applyStageFailed
	s.applyStatus.Err = err
	s.applyMu.Unlock()
}

// runApplyUpdate 是實際執行「抓 Manifest -> 找出這台機器對應的 asset ->
// 下載並驗證 checksum -> 原子置換執行檔 -> 通知 main.go 重啟」這一整串
// 步驟的背景 goroutine,由 handleSystemUpdateApply 觸發。任何一步失敗都
// 會透過 failApply 記錄原因並直接返回,不會有「部分套用」的狀態——見
// internal/selfupdate.ApplyUpdate 的說明,置換這一步本身要嘛完整成功、
// 要嘛把備份檔案還原回去。
func (s *Server) runApplyUpdate(ctx context.Context, manifestURL string) {
	manifest, err := selfupdate.FetchManifest(ctx, s.updateHTTPClient, manifestURL)
	if err != nil {
		s.failApply(err)
		return
	}

	asset, err := manifest.AssetFor(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		s.failApply(err)
		return
	}

	execPath, err := s.resolveExecPath()
	if err != nil {
		s.failApply(err)
		return
	}

	s.setApplyStage(applyStageDownloading)
	tempPath, err := selfupdate.DownloadAndVerify(ctx, s.updateHTTPClient, asset, filepath.Dir(execPath))
	if err != nil {
		s.failApply(err)
		return
	}

	s.setApplyStage(applyStageApplying)
	backupPath, err := selfupdate.ApplyUpdate(execPath, tempPath)
	if err != nil {
		s.failApply(err)
		return
	}
	s.logger.Info("self-update applied, requesting restart", "newVersion", manifest.Version, "backupPath", backupPath)

	s.setApplyStage(applyStageRestarting)
	// 送出 execPath(在 ApplyUpdate 置換之前就解析好的那個路徑),不是
	// 事後讓 main.go 自己重新查——見 restartRequested 欄位註解裡解釋的
	// /proc/self/exe 坑。非阻塞送出:cmd/gonasd/main.go 一定有在 select
	// 這個 channel,但這個 channel 是 buffered size 1,就算極端情況下
	// 真的沒有人在讀,也不該讓這個背景 goroutine 卡住。
	select {
	case s.restartRequested <- execPath:
	default:
	}
}

// handleSystemUpdateRollback 是 handleSystemUpdateApply 的反向操作:把
// 執行檔還原成 ApplyUpdate 換掉之前保留的 ".previous" 備份,然後跟
// apply 一樣重啟整個 gonasd 程序。用途是「套用了新版本之後發現有問題,
// 想馬上退回上一個能動的版本」——不需要使用者自己上機器手動搬檔案。
//
// 跟 apply 共用同一個 s.applyMu/s.applyStatus 狀態機(rollback 也會經過
// applyStageApplying/applyStageRestarting 等階段),因為兩者在「會不會
// 撞在一起執行」這件事上是同一個問題:置換執行檔跟重啟這兩步不能跟
// 另一次 apply 或另一次 rollback 同時進行。
func (s *Server) handleSystemUpdateRollback(w http.ResponseWriter, r *http.Request) {
	execPath, err := s.resolveExecPath()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if _, statErr := os.Stat(execPath + ".previous"); statErr != nil {
		writeError(w, http.StatusBadRequest, errUpdateNoBackupAvailable)
		return
	}

	s.applyMu.Lock()
	if s.applyStatus.Stage != applyStageIdle && s.applyStatus.Stage != applyStageFailed {
		s.applyMu.Unlock()
		writeError(w, http.StatusConflict, errUpdateAlreadyInProgress)
		return
	}
	s.applyStatus = applyUpdateStatus{Stage: applyStageApplying, StartedAt: time.Now()}
	s.applyMu.Unlock()

	// 跟 handleSystemUpdateApply 一樣用 context.Background():置換檔案跟
	// 重啟這兩步不該因為使用者關掉瀏覽器分頁就半途而廢。
	go s.runRollback(execPath)

	writeJSON(w, http.StatusAccepted, systemUpdateApplyResponse{
		Message: "正在復原到上一個版本,完成後 gonasd 會自動重新啟動,請稍後重新整理頁面。",
	})
}

// runRollback 是 handleSystemUpdateRollback 觸發的背景 goroutine,實際
// 呼叫 selfupdate.RollbackToBackup 把備份換回來,然後跟 runApplyUpdate
// 一樣送出 execPath 請求重啟。execPath 是呼叫端(handleSystemUpdateRollback)
// 在啟動這個 goroutine之前就解析好的那個路徑,原因跟 runApplyUpdate 裡
// 送出 execPath 給 restartRequested 的原因完全一樣——rollback 一樣會
// 把目前執行檔搬走(RollbackToBackup 內部先把它改名成
// ".rolled-back-<unix>"),所以同一個 /proc/self/exe 陷阱在這裡也適用,
// 不能讓重啟後的邏輯再自己重新呼叫 os.Executable()。
func (s *Server) runRollback(execPath string) {
	rolledBackFromPath, err := selfupdate.RollbackToBackup(execPath)
	if err != nil {
		s.failApply(err)
		return
	}
	s.logger.Info("self-update rolled back, requesting restart", "rolledBackFromPath", rolledBackFromPath)

	s.setApplyStage(applyStageRestarting)
	select {
	case s.restartRequested <- execPath:
	default:
	}
}
