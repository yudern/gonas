package api

import (
	"context"
	"net/http"
	"time"

	"github.com/bng147/gonas/internal/appstore"
	"github.com/bng147/gonas/internal/state"
)

func (s *Server) handleAppstoreCatalog(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, builtinCatalog)
}

func (s *Server) handleAppstoreListInstalled(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.store.Snapshot().InstalledApps)
}

// installAppRequest 是 POST /api/v1/appstore/apps 的請求 body。恰好要帶
// TemplateID 或 Template 兩者之一:
//   - TemplateID 對應 builtinCatalog 裡的範本(內建的 Portainer/
//     code-server/WordPress 這幾個)。
//   - Template 是使用者自己填的完整 AppTemplate —— 不透過內建目錄,直接
//     指定任意 image/埠/掛載/環境變數,對應 Unraid「新增容器」那種
//     不套用任何 Community Applications 範本、自己填全部欄位的安裝
//     方式。兩種路徑最後都餵給同一組 appstore.Install/Uninstall 邏輯,
//     安裝完之後兩者在 state.InstalledApps 裡看起來完全一樣,Uninstall
//     不需要區分這個 App 當初是怎麼裝進來的。
type installAppRequest struct {
	TemplateID string                              `json:"templateId,omitempty"`
	Template   *appstore.AppTemplate               `json:"template,omitempty"`
	Overrides  map[string]appstore.ServiceOverride `json:"overrides"`
}

// resolveInstallTemplate 決定這次安裝到底要用哪個範本,並確保自訂範本的
// ID 不會撞到內建目錄或現有已安裝的 App —— App ID 同時也是容器命名
// 前綴、專屬網路名稱、Uninstall 時拿來過濾 Docker 標籤的 key(見
// internal/appstore.Uninstall 的說明),ID 撞名會讓 Uninstall 誤刪不
// 相干的 App，這個檢查不能省。
func (s *Server) resolveInstallTemplate(req installAppRequest) (appstore.AppTemplate, error) {
	hasID := req.TemplateID != ""
	hasTemplate := req.Template != nil
	if hasID == hasTemplate {
		return appstore.AppTemplate{}, errAppInstallNeedsExactlyOne
	}

	if hasID {
		for i := range builtinCatalog {
			if builtinCatalog[i].ID == req.TemplateID {
				// 第六十輪 QA 覆核:目錄範本也要擋「已經裝過同一個」——原本只對
				// 自訂範本檢查,結果重複安裝目錄 App(或手殘點兩下)會一路跑到
				// docker 才以「容器名稱已存在」爆成 500。提前回可讀的 409。
				for _, installed := range s.store.Snapshot().InstalledApps {
					if installed.Template.ID == builtinCatalog[i].ID {
						return appstore.AppTemplate{}, errAppIDAlreadyInstalled
					}
				}
				return builtinCatalog[i], nil
			}
		}
		return appstore.AppTemplate{}, errAppNotFound
	}

	tmpl := *req.Template
	for i := range builtinCatalog {
		if builtinCatalog[i].ID == tmpl.ID {
			return appstore.AppTemplate{}, errAppIDConflictsWithCatalog
		}
	}
	for _, installed := range s.store.Snapshot().InstalledApps {
		if installed.Template.ID == tmpl.ID {
			return appstore.AppTemplate{}, errAppIDAlreadyInstalled
		}
	}
	return tmpl, nil
}

func (s *Server) handleAppstoreInstall(w http.ResponseWriter, r *http.Request) {
	var req installAppRequest
	if !readJSON(w, r, &req) {
		return
	}

	tmpl, err := s.resolveInstallTemplate(req)
	if err != nil {
		status := http.StatusBadRequest
		if err == errAppNotFound {
			status = http.StatusNotFound
		} else if err == errAppIDConflictsWithCatalog || err == errAppIDAlreadyInstalled {
			status = http.StatusConflict
		}
		writeError(w, status, err)
		return
	}

	// single-flight(第六十輪 QA 覆核):安裝要拉映像、建容器,可能好幾分鐘,
	// 同一時間只允許一個,避免並發撞容器命名/網路或雙重寫入 InstalledApps。
	if !s.appInstalling.CompareAndSwap(false, true) {
		writeError(w, http.StatusConflict, errAppInstallInProgress)
		return
	}
	defer s.appInstalling.Store(false)

	// 跟 HTTP 請求脫鉤的 context(第六十輪 QA 覆核):拉映像可能好幾分鐘,若綁
	// r.Context(),使用者一關瀏覽器/連線一斷,安裝會被砍在半路,而且連內部的
	// rollback 清理都會因為 context 已取消而失敗,留下半裝的容器/網路。給獨立、
	// 30 分鐘上限的 context 讓它跑完;前端顯示「安裝中,可能需要幾分鐘」。
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	result, err := appstore.Install(ctx, s.docker, appstore.InstallRequest{
		Template:         tmpl,
		Overrides:        req.Overrides,
		StartGracePeriod: 2 * time.Second,
		OnRollbackError: func(svc string, rbErr error) {
			// 安裝失敗後的回滾清理若又出錯,記下來——不然會留下沒清乾淨的
			// 容器/網路卻無跡可尋(第三十四輪)。
			s.logger.Warn("app install rollback cleanup failed", "app", tmpl.ID, "service", svc, "err", rbErr)
		},
	})
	if err != nil {
		s.logger.Error("installing app failed", "app", tmpl.ID, "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	installed := state.InstalledApp{Template: tmpl, Result: result, Overrides: req.Overrides}
	if err := s.store.Update(func(st *state.State) error {
		st.InstalledApps = append(st.InstalledApps, installed)
		return nil
	}); err != nil {
		// 容器已經真的裝起來了，只是記錄沒寫進去 —— 回傳成功但把這個問題記進
		// log,好過因為記錄失敗就假裝安裝沒發生（那樣使用者會裝出孤兒容器,
		// 之後靠 Uninstall 的標籤掃描還是找得回來，只是列表暫時看不到)。
		s.logger.Error("app installed but persisting installed-app record failed", "app", tmpl.ID, "err", err)
	}

	writeJSON(w, http.StatusOK, installed)
}

func (s *Server) handleAppstoreUninstall(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	// 跟安裝共用同一個 single-flight 旗標(第六十輪):不要讓解除安裝跟安裝
	// (或另一個解除安裝)同時動 docker 資源與 InstalledApps。
	if !s.appInstalling.CompareAndSwap(false, true) {
		writeError(w, http.StatusConflict, errAppInstallInProgress)
		return
	}
	defer s.appInstalling.Store(false)

	// 第五十八輪 QA 覆核(#6):先確認這個 app 真的裝過,不然回 404——跟
	// shares/users/backup jobs/peers 的刪除行為一致(原本對不存在的 id 也
	// 回 204,會讓前端以為「本來就有、已刪掉」)。
	found := false
	for _, app := range s.store.Snapshot().InstalledApps {
		if app.Template.ID == id {
			found = true
			break
		}
	}
	if !found {
		writeError(w, http.StatusNotFound, errAppNotFound)
		return
	}

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
