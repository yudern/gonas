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

// appOpStatus 是一次安裝/更新的進度快照,給前端輪詢(GET /appstore/op-status)。
type appOpStatus struct {
	AppID    string `json:"appId"`
	Action   string `json:"action"`             // "install" | "update"
	Service  string `json:"service,omitempty"`  // 目前正在拉/建哪個服務
	Progress string `json:"progress,omitempty"` // docker 拉取進度行
	Stage    string `json:"stage"`              // "running" | "done" | "failed"
	Error    string `json:"error,omitempty"`
}

func (s *Server) setAppOp(st appOpStatus) { s.appOpStatus.Store(&st) }

// handleAppstoreOpStatus 回傳目前(或剛結束)那次安裝/更新的進度。沒有任何
// 進行過就回 stage:"idle"。前端據此顯示進度條、完成或失敗。requireAuth。
func (s *Server) handleAppstoreOpStatus(w http.ResponseWriter, r *http.Request) {
	if st := s.appOpStatus.Load(); st != nil {
		writeJSON(w, http.StatusOK, st)
		return
	}
	writeJSON(w, http.StatusOK, appOpStatus{Stage: "idle"})
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

	// 第六十輪產品覆核:改成「背景執行 + 回 202 + 前端輪詢進度」。原本整個安裝
	// (拉映像可能好幾分鐘)同步擋在這支請求上,前端只能空等、沒有進度。現在
	// 背景跑,OnPullProgress 把 docker 的拉取進度寫進 appOpStatus,前端輪詢
	// GET /appstore/op-status 顯示。single-flight 旗標由背景 goroutine 結束時釋放。
	s.setAppOp(appOpStatus{AppID: tmpl.ID, Action: "install", Stage: "running"})
	overrides := req.Overrides
	go func() {
		defer s.appInstalling.Store(false)
		// 跟 HTTP 請求脫鉤的 context:拉映像可能好幾分鐘,使用者關瀏覽器也不該
		// 把安裝砍在半路(連內部 rollback 都會被取消的 context 搞壞)。
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()

		result, err := appstore.Install(ctx, s.docker, appstore.InstallRequest{
			Template:         tmpl,
			Overrides:        overrides,
			StartGracePeriod: 2 * time.Second,
			OnPullProgress: func(svc, status string) {
				s.setAppOp(appOpStatus{AppID: tmpl.ID, Action: "install", Stage: "running", Service: svc, Progress: status})
			},
			OnRollbackError: func(svc string, rbErr error) {
				s.logger.Warn("app install rollback cleanup failed", "app", tmpl.ID, "service", svc, "err", rbErr)
			},
		})
		if err != nil {
			s.logger.Error("installing app failed", "app", tmpl.ID, "err", err)
			s.setAppOp(appOpStatus{AppID: tmpl.ID, Action: "install", Stage: "failed", Error: err.Error()})
			return
		}
		installed := state.InstalledApp{Template: tmpl, Result: result, Overrides: overrides}
		if err := s.store.Update(func(st *state.State) error {
			st.InstalledApps = append(st.InstalledApps, installed)
			return nil
		}); err != nil {
			s.logger.Error("app installed but persisting installed-app record failed", "app", tmpl.ID, "err", err)
		}
		s.setAppOp(appOpStatus{AppID: tmpl.ID, Action: "install", Stage: "done"})
	}()

	writeJSON(w, http.StatusAccepted, map[string]string{"status": "installing", "appId": tmpl.ID})
}

// handleAppstoreUpdate 更新一個已安裝的 App:重拉映像、用原本的設定重建容器
// (保留資料)。第六十輪產品覆核:原本 App 裝好就凍結,想升級只能解除安裝再
// 重裝、重填所有設定。這裡沿用安裝當下存下的 Overrides(見 InstalledApp.Overrides)
// 重建,使用者不必重填。requireAdmin。
func (s *Server) handleAppstoreUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	// 第六十輪複審:先搶 single-flight,再查。原本先查、後搶,而且在迴圈裡重複
	// 呼叫 Snapshot() 並沿用索引——跟並發的 handleAppstoreUninstall(會原地縮短
	// InstalledApps 切片)撞在一起可能 index out of range panic / data race。先搶旗標
	// 把安裝/解除安裝/更新互斥起來,再用「單一一份」snapshot 查,就沒有這個窗口。
	if !s.appInstalling.CompareAndSwap(false, true) {
		writeError(w, http.StatusConflict, errAppInstallInProgress)
		return
	}

	var app *state.InstalledApp
	for _, a := range s.store.Snapshot().InstalledApps {
		if a.Template.ID == id {
			ac := a
			app = &ac
			break
		}
	}
	if app == nil {
		s.appInstalling.Store(false)
		writeError(w, http.StatusNotFound, errAppNotFound)
		return
	}

	// 跟安裝一樣:背景執行 + 進度 + 回 202(更新也會拉映像,可能好幾分鐘)。
	s.setAppOp(appOpStatus{AppID: id, Action: "update", Stage: "running"})
	appCopy := *app
	go func() {
		defer s.appInstalling.Store(false)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()

		s.logger.Info("updating app", "app", id)
		result, err := appstore.Update(ctx, s.docker, appstore.InstallRequest{
			Template:         appCopy.Template,
			Overrides:        appCopy.Overrides,
			StartGracePeriod: 2 * time.Second,
			OnPullProgress: func(svc, status string) {
				s.setAppOp(appOpStatus{AppID: id, Action: "update", Stage: "running", Service: svc, Progress: status})
			},
			OnRollbackError: func(svc string, rbErr error) {
				s.logger.Warn("app update rollback cleanup failed", "app", id, "service", svc, "err", rbErr)
			},
		})
		if err != nil {
			s.logger.Error("updating app failed", "app", id, "err", err)
			// 若「移除舊容器之後、重建失敗」:舊容器已不在,記錄卻還指著它們。
			// 用 label 掃描判斷容器是否真的沒了,沒了就移除殭屍記錄。
			stillThere := false
			if cs, lerr := s.docker.ListContainers(ctx, true); lerr == nil {
				for _, c := range cs {
					if c.Labels["com.gonas.app"] == id {
						stillThere = true
						break
					}
				}
			} else {
				stillThere = true
			}
			if !stillThere {
				if uerr := s.store.Update(func(st *state.State) error {
					kept := st.InstalledApps[:0]
					for _, a := range st.InstalledApps {
						if a.Template.ID != id {
							kept = append(kept, a)
						}
					}
					st.InstalledApps = kept
					return nil
				}); uerr != nil {
					s.logger.Error("failed to remove the stale app record after a failed update", "app", id, "err", uerr)
				}
			}
			s.setAppOp(appOpStatus{AppID: id, Action: "update", Stage: "failed", Error: err.Error()})
			return
		}
		updated := state.InstalledApp{Template: appCopy.Template, Result: result, Overrides: appCopy.Overrides}
		if err := s.store.Update(func(st *state.State) error {
			for i := range st.InstalledApps {
				if st.InstalledApps[i].Template.ID == id {
					st.InstalledApps[i] = updated
					return nil
				}
			}
			st.InstalledApps = append(st.InstalledApps, updated)
			return nil
		}); err != nil {
			s.logger.Error("app updated but persisting the new record failed", "app", id, "err", err)
		}
		s.setAppOp(appOpStatus{AppID: id, Action: "update", Stage: "done"})
	}()

	writeJSON(w, http.StatusAccepted, map[string]string{"status": "updating", "appId": id})
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
