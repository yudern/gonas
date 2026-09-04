package api

import (
	"net/http"
	"os"
	"path/filepath"

	"github.com/bng147/gonas/internal/share"
	"github.com/bng147/gonas/internal/state"
)

// 這兩個路徑對應 internal/share 套件文件裡說的「GoNAS 自己管理的 include
// 檔案」。真正的系統設定(/etc/samba/smb.conf、/etc/exports)不會被寫到。
const (
	sambaConfigPath   = "/etc/samba/gonas-shares.conf"
	exportsConfigPath = "/etc/exports.d/gonas.exports"
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
		st.Exports = append(st.Exports, exp)
		allExports = append([]share.Export{}, st.Exports...)
		return nil
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	applied, warn := s.applyExportsConfig(r, allExports)
	writeJSON(w, http.StatusOK, struct {
		share.Export
		applyResult
	}{exp, applyResult{Applied: applied, Warning: warn}})
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

	found := false
	if err := s.store.Update(func(st *state.State) error {
		kept := st.Users[:0]
		for _, u := range st.Users {
			if u.Username == username {
				found = true
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
	if !found {
		writeError(w, http.StatusNotFound, errUserNotFound)
		return
	}

	if err := share.DeleteUser(r.Context(), s.runner, username); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
