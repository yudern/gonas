package api

import (
	"net/http"

	"github.com/bng147/gonas/internal/appstore"
	"github.com/bng147/gonas/internal/state"
)

func (s *Server) handleAppstoreCatalog(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, builtinCatalog)
}

func (s *Server) handleAppstoreListInstalled(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.store.Snapshot().InstalledApps)
}

// installAppRequest 是 POST /api/v1/appstore/apps 的請求 body。TemplateID
// 對應 builtinCatalog 裡的範本(之後接上遠端目錄或自訂範本時,這裡也可以
// 改成直接接受完整的 AppTemplate,兩者用同一組 Overrides/Install 邏輯)。
type installAppRequest struct {
	TemplateID string                              `json:"templateId"`
	Overrides  map[string]appstore.ServiceOverride `json:"overrides"`
}

func (s *Server) handleAppstoreInstall(w http.ResponseWriter, r *http.Request) {
	var req installAppRequest
	if !readJSON(w, r, &req) {
		return
	}

	var tmpl *appstore.AppTemplate
	for i := range builtinCatalog {
		if builtinCatalog[i].ID == req.TemplateID {
			tmpl = &builtinCatalog[i]
			break
		}
	}
	if tmpl == nil {
		writeError(w, http.StatusNotFound, errAppNotFound)
		return
	}

	result, err := appstore.Install(r.Context(), s.docker, appstore.InstallRequest{
		Template:  *tmpl,
		Overrides: req.Overrides,
	})
	if err != nil {
		s.logger.Error("installing app failed", "app", req.TemplateID, "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	installed := state.InstalledApp{Template: *tmpl, Result: result}
	if err := s.store.Update(func(st *state.State) error {
		st.InstalledApps = append(st.InstalledApps, installed)
		return nil
	}); err != nil {
		// 容器已經真的裝起來了，只是記錄沒寫進去 —— 回傳成功但把這個問題記進
		// log,好過因為記錄失敗就假裝安裝沒發生（那樣使用者會裝出孤兒容器,
		// 之後靠 Uninstall 的標籤掃描還是找得回來，只是列表暫時看不到)。
		s.logger.Error("app installed but persisting installed-app record failed", "app", req.TemplateID, "err", err)
	}

	writeJSON(w, http.StatusOK, installed)
}

func (s *Server) handleAppstoreUninstall(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	if err := appstore.Uninstall(r.Context(), s.docker, id); err != nil {
		s.logger.Error("uninstalling app failed", "app", id, "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	if err := s.store.Update(func(st *state.State) error {
		kept := st.InstalledApps[:0]
		for _, app := range st.InstalledApps {
			if app.Template.ID != id {
				kept = append(kept, app)
			}
		}
		st.InstalledApps = kept
		return nil
	}); err != nil {
		s.logger.Error("app uninstalled but removing its record failed", "app", id, "err", err)
	}

	w.WriteHeader(http.StatusNoContent)
}
