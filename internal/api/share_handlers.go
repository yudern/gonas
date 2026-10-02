package api

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/bng147/gonas/internal/share"
	"github.com/bng147/gonas/internal/state"
)

// runRecycleCleanup 是每天一次的 SMB 回收筒清理:對每個開了回收筒且設了保留
// 天數(RecycleMaxDays>0)的共享,刪掉回收筒裡待超過保留天數的檔案。讀當下
// 的共享清單,沒有符合的就什麼都不做。best-effort:個別共享清理失敗只記 log,
// 不影響其他共享。由背景排程(recycleCleanup)呼叫。
func (s *Server) runRecycleCleanup(_ context.Context) error {
	now := time.Now()
	for _, sh := range s.store.Snapshot().Shares {
		if !sh.Recycle || sh.RecycleMaxDays <= 0 {
			continue
		}
		maxAge := time.Duration(sh.RecycleMaxDays) * 24 * time.Hour
		removed, err := share.PruneRecycleBin(sh.Path, maxAge, now)
		if err != nil {
			s.logger.Warn("recycle bin cleanup failed for a share", "share", sh.Name, "err", err)
			continue
		}
		if removed > 0 {
			s.logger.Info("recycle bin cleanup removed expired files", "share", sh.Name, "removed", removed, "maxDays", sh.RecycleMaxDays)
		}
	}
	return nil
}

// 這兩個路徑對應 internal/share 套件文件裡說的「GoNAS 自己管理的 include
// 檔案」。真正的系統設定(/etc/samba/smb.conf、/etc/exports)不會被寫到。
const (
	sambaConfigPath   = "/etc/samba/gonas-shares.conf"
	exportsConfigPath = "/etc/exports.d/gonas.exports"
	// smbConfPath 是系統原生的 Samba 主設定檔——我們不覆寫它,只確保它有一行
	// `include = <sambaConfigPath>` 把 GoNAS 管理的共享檔引入(見
	// share.EnsureSambaInclude 與第五十八輪產品覆核 P2)。
	smbConfPath = "/etc/samba/smb.conf"
)

// applyResult 讓 Web UI 能區分「設定已經存好了」跟「而且也已經套用到正在
// 跑的服務」——後者需要 testparm/smbd、exportfs 這些工具實際存在,在
// 服務還沒裝好的機器上（例如全新安裝、還沒跑過套件安裝步驟)這步驟失敗是
// 預期內的情況，不該讓整個請求連「存設定」都一起失敗。
type applyResult struct {
	Applied bool   `json:"applied"`
	Warning string `json:"warning,omitempty"`
}

func (s *Server) handleSharesList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.store.Snapshot().Shares)
}

func (s *Server) handleSharesCreate(w http.ResponseWriter, r *http.Request) {
	var sh share.Share
	if !readJSON(w, r, &sh) {
		return
	}
	if err := sh.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	// 第五十五輪覆核(產品 P2):共享指向的資料夾要先建出來,否則 smb.conf 雖然
	// 寫好了,Windows/Mac 連進來只會得到「找不到網路名稱」(BAD_NETWORK_NAME)。
	// 正常流程此時陣列已掛在 /mnt/tank,mkdir 會落在 mergerfs union 上;
	// best-effort:建不了(例如陣列還沒掛)不擋住建立共享——設定還是會存,
	// 資料夾之後(啟動陣列或手動)補上即可。
	if err := os.MkdirAll(sh.Path, 0o2775); err != nil {
		s.logger.Warn("could not create the share folder (share config still saved)", "path", sh.Path, "err", err)
	}

	var allShares []share.Share
	if err := s.store.Update(func(st *state.State) error {
		for _, existing := range st.Shares {
			if existing.Name == sh.Name {
				return errShareAlreadyExists
			}
		}
		st.Shares = append(st.Shares, sh)
		allShares = append([]share.Share{}, st.Shares...)
		return nil
	}); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}

	applied, warn := s.applySambaConfig(r, allShares)
	writeJSON(w, http.StatusOK, struct {
		share.Share
		applyResult
	}{sh, applyResult{Applied: applied, Warning: warn}})
}

func (s *Server) handleSharesDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	var allShares []share.Share
	found := false
	if err := s.store.Update(func(st *state.State) error {
		kept := st.Shares[:0]
		for _, sh := range st.Shares {
			if sh.Name == name {
				found = true
				continue
			}
			kept = append(kept, sh)
		}
		st.Shares = kept
		allShares = append([]share.Share{}, st.Shares...)
		return nil
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, errShareNotFound)
		return
	}

	applied, warn := s.applySambaConfig(r, allShares)
	writeJSON(w, http.StatusOK, applyResult{Applied: applied, Warning: warn})
}

// ensureServicesRunning 對每個服務 best-effort 執行 `systemctl enable --now`,
// 讓服務「現在就起來、且開機自動起」。第六十輪(安全/構建覆核):appliance
// 刻意不在安裝階段自動啟用 samba/nfs 這類守護進程(避免使用者還沒設定任何
// 共享,服務就先在網路上監聽);改成「使用者真的建立共享時,GoNAS 才把對應
// 服務拉起來並設為開機啟用」——跟 Doctor 裝好 docker.io 後 enable --now docker
// 是同一個模式。沒裝那個服務就會失敗,但這裡是 best-effort(只記 log),後面
// 的 Reload 失敗會給使用者看得懂的「是否已安裝」警告,不在這裡擋。
func (s *Server) ensureServicesRunning(ctx context.Context, names ...string) {
	for _, name := range names {
		if out, err := s.runner.Run(ctx, "systemctl", "enable", "--now", name); err != nil {
			s.logger.Warn("could not enable/start service (may not be installed yet)", "service", name, "err", err, "out", string(out))
		}
	}
}

// applySambaConfig 把目前完整的共享清單重新產生成設定檔、寫入、並嘗試
// 通知 smbd 重新讀取。任何一步失敗都只回傳警告，不會讓呼叫端誤以為
// 「共享沒有存成功」——存到 state store 才是唯一的事實來源。
func (s *Server) applySambaConfig(r *http.Request, shares []share.Share) (applied bool, warning string) {
	content, err := share.GenerateSambaConfig(shares, sambaConfigPath)
	if err != nil {
		return false, err.Error()
	}
	if err := os.MkdirAll(filepath.Dir(sambaConfigPath), 0o755); err != nil {
		return false, "creating samba config directory: " + err.Error()
	}
	if err := share.WriteConfigAtomically(sambaConfigPath, content); err != nil {
		return false, err.Error()
	}
	// 第五十八輪(產品 P2):確保 smb.conf 真的 include 了我們的共享檔,否則
	// smbd 永遠讀不到、共享在網路上看不到。best-effort:補不了也只回警告。
	if err := share.EnsureSambaInclude(smbConfPath, sambaConfigPath); err != nil {
		return false, "config saved, but wiring it into smb.conf failed (is samba installed?): " + err.Error()
	}
	// 第六十輪:建立共享時才把 samba 服務拉起來+設開機啟用(appliance 預設不自動
	// 啟用,見 ensureServicesRunning)。best-effort,沒裝 samba 就由下面的 reload
	// 給出「是否已安裝」的警告。
	s.ensureServicesRunning(r.Context(), "smbd", "nmbd")
	if err := share.ReloadSamba(r.Context(), s.runner); err != nil {
		return false, "config saved, but reloading smbd failed (is samba installed and running?): " + err.Error()
	}
	return true, ""
}

func (s *Server) handleExportsList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.store.Snapshot().Exports)
}

func (s *Server) handleExportsCreate(w http.ResponseWriter, r *http.Request) {
	var exp share.Export
	if !readJSON(w, r, &exp) {
		return
	}
	if err := exp.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	var allExports []share.Export
	if err := s.store.Update(func(st *state.State) error {
		// 第五十八輪 QA 覆核(#5):跟 SMB 共享用 Name 擋重複一樣,NFS export
		// 用 Path 擋重複——否則同一路徑會產生重複的 export 行,而刪除是依 Path
		// 過濾、會一次把重複的全刪掉,行為不一致。
		for _, e := range st.Exports {
			if e.Path == exp.Path {
				return errExportAlreadyExists
			}
		}
		st.Exports = append(st.Exports, exp)
		allExports = append([]share.Export{}, st.Exports...)
		return nil
	}); err != nil {
		if err == errExportAlreadyExists {
			writeError(w, http.StatusConflict, err)
		} else {
			writeError(w, http.StatusInternalServerError, err)
		}
		return
	}

	applied, warn := s.applyExportsConfig(r, allExports)
	writeJSON(w, http.StatusOK, struct {
		share.Export
		applyResult
	}{exp, applyResult{Applied: applied, Warning: warn}})
}

// handleExportsDelete 刪掉一筆 NFS export。export 用 Path 當識別(路徑含斜線,
// 不方便放進 URL path 段),所以從 query 參數 ?path=/mnt/tank/media 取。
// 第五十六輪覆核(產品 P3):原本 NFS export 只能新增不能刪,是個死路——加了
// 這支端點與前端的刪除按鈕,跟 SMB 共享對稱。
func (s *Server) handleExportsDelete(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		writeError(w, http.StatusBadRequest, errExportPathRequired)
		return
	}

	var allExports []share.Export
	found := false
	if err := s.store.Update(func(st *state.State) error {
		kept := st.Exports[:0]
		for _, e := range st.Exports {
			if e.Path == path {
				found = true
				continue
			}
			kept = append(kept, e)
		}
		st.Exports = kept
		allExports = append([]share.Export{}, st.Exports...)
		return nil
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, errExportNotFound)
		return
	}

	applied, warn := s.applyExportsConfig(r, allExports)
	writeJSON(w, http.StatusOK, applyResult{Applied: applied, Warning: warn})
}

func (s *Server) applyExportsConfig(r *http.Request, exports []share.Export) (applied bool, warning string) {
	content, err := share.GenerateExportsConfig(exports)
	if err != nil {
		return false, err.Error()
	}
	if err := os.MkdirAll(filepath.Dir(exportsConfigPath), 0o755); err != nil {
		return false, "creating exports config directory: " + err.Error()
	}
	if err := share.WriteConfigAtomically(exportsConfigPath, content); err != nil {
		return false, err.Error()
	}
	// 第六十輪:建立 NFS 匯出時才把 nfs 服務拉起來+設開機啟用(同 samba)。
	s.ensureServicesRunning(r.Context(), "nfs-kernel-server")
	if err := share.ReloadNFS(r.Context(), s.runner); err != nil {
		return false, "config saved, but reloading nfs exports failed (is nfs-kernel-server installed and running?): " + err.Error()
	}
	return true, ""
}

func (s *Server) handleUsersList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.store.Snapshot().Users)
}

type createUserRequest struct {
	Username string `json:"username"`
	Comment  string `json:"comment,omitempty"`
	Password string `json:"password"`
}

func (s *Server) handleUsersCreate(w http.ResponseWriter, r *http.Request) {
	var req createUserRequest
	if !readJSON(w, r, &req) {
		return
	}
	u := share.User{Username: req.Username, Comment: req.Comment}
	if err := u.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Password == "" {
		writeError(w, http.StatusBadRequest, errPasswordRequired)
		return
	}
	// 密碼安全檢查要在「建立帳號之前」就擋掉,回 400 —— 而不是等到
	// SetSystemPassword 才失敗回 500(那樣還得回滾剛建好的帳號)。這條擋的是
	// 第三十輪覆核抓到的 chpasswd 換行注入(密碼含 \n 可改掉別的帳號密碼)。
	if err := share.ValidatePassword(req.Password); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	if err := share.CreateUser(r.Context(), s.runner, u); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if err := share.SetSystemPassword(r.Context(), s.runner, u.Username, req.Password); err != nil {
		// 帳號已經建立但密碼沒設成功：回報錯誤，但不把帳號記進 state,
		// 讓使用者知道要重試，而不是留下一個「看起來裝好了但密碼是空的」帳號。
		_ = share.DeleteUser(r.Context(), s.runner, u.Username)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	// Samba 密碼同步是「盡量而為」：這個系統可能根本沒裝 Samba,失敗不該讓
	// 整個帳號建立流程回滾（系統帳號本身、NFS 存取都已經是有效狀態了）。
	sambaWarning := ""
	if err := share.SyncSambaPassword(r.Context(), s.runner, u.Username, req.Password); err != nil {
		sambaWarning = "user created, but syncing samba password failed (is samba installed?): " + err.Error()
	}

	record := state.UserRecord{Username: u.Username, Comment: u.Comment}
	if err := s.store.Update(func(st *state.State) error {
		st.Users = append(st.Users, record)
		return nil
	}); err != nil {
		s.logger.Error("user created but persisting user record failed", "user", u.Username, "err", err)
	}

	writeJSON(w, http.StatusOK, struct {
		state.UserRecord
		SambaWarning string `json:"sambaWarning,omitempty"`
	}{record, sambaWarning})
}

func (s *Server) handleUsersDelete(w http.ResponseWriter, r *http.Request) {
	username := r.PathValue("username")

	// 先確認這個使用者存在(回正確的 404),但先不動 state。
	exists := false
	for _, u := range s.store.Snapshot().Users {
		if u.Username == username {
			exists = true
			break
		}
	}
	if !exists {
		writeError(w, http.StatusNotFound, errUserNotFound)
		return
	}

	// 第五十六輪覆核(QA4):先刪系統/Samba 帳號,成功了才從 state 移除。原本
	// 順序相反:state 先移除、若 userdel 失敗,GoNAS 清單上看不到這個使用者了,
	// 但 Linux/Samba 帳號還在、還能登入(孤兒帳號、看不見卻可認證)。反過來做,
	// 最壞情況只是「清單還在、可重試」,不會留下隱形的可登入帳號。
	if err := share.DeleteUser(r.Context(), s.runner, username); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	if err := s.store.Update(func(st *state.State) error {
		kept := st.Users[:0]
		for _, u := range st.Users {
			if u.Username == username {
				continue
			}
			kept = append(kept, u)
		}
		st.Users = kept
		return nil
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
