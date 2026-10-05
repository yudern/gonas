package api

import (
	"net/http"
	"time"

	"github.com/bng147/gonas/internal/docker"
	"github.com/bng147/gonas/internal/state"
)

// checkUpdateServiceResult 是單一服務的更新檢查結果。
type checkUpdateServiceResult struct {
	Service         string `json:"service"`
	Image           string `json:"image"`
	UpdateAvailable bool   `json:"updateAvailable"`
	Error           string `json:"error,omitempty"`
}

type checkUpdateResponse struct {
	UpdateAvailable bool                       `json:"updateAvailable"` // 任一服務有新版就 true
	Checked         bool                       `json:"checked"`         // 至少有一個服務成功查到結果
	Services        []checkUpdateServiceResult `json:"services"`
}

// handleAppstoreCheckUpdate 檢查一個已安裝 App 的每個服務映像,在 registry 上是否
// 有比本機更新的版本(比對 manifest 摘要,不下載任何層)。requireAdmin。
//
// 直連 registry、不走 dockerd 的鏡像加速器,所以連不上 Docker Hub 的網路會回
// 每個服務一個 error、UpdateAvailable=false、Checked=false —— 前端據此顯示
// 「無法檢查」而不是「已是最新」(避免誤導)。真正的更新走 handleAppstoreUpdate
// (dockerd pull,會用加速器)。
func (s *Server) handleAppstoreCheckUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var app *state.InstalledApp
	for _, a := range s.store.Snapshot().InstalledApps {
		if a.Template.ID == id {
			ac := a
			app = &ac
			break
		}
	}
	if app == nil {
		writeError(w, http.StatusNotFound, errAppNotFound)
		return
	}
	if s.docker == nil {
		writeError(w, http.StatusServiceUnavailable, errAppNotFound)
		return
	}

	client := &http.Client{Timeout: 20 * time.Second}
	resp := checkUpdateResponse{}
	for _, svc := range app.Template.Services {
		image := app.Overrides[svc.Name].EffectiveImage(svc.Image)
		res := checkUpdateServiceResult{Service: svc.Name, Image: image}

		ref := docker.ParseImageRef(image)
		if ref.IsDigest {
			// 已用 @sha256 釘死版本,沒有「新版本」的概念。
			resp.Services = append(resp.Services, res)
			resp.Checked = true
			continue
		}

		local, err := s.docker.LocalImageDigest(r.Context(), image)
		if err != nil {
			res.Error = "读取本机镜像摘要失败:" + err.Error()
			resp.Services = append(resp.Services, res)
			continue
		}
		remote, err := docker.FetchRemoteDigest(r.Context(), client, ref)
		if err != nil {
			res.Error = "查询 registry 失败(可能连不上 Docker Hub):" + err.Error()
			resp.Services = append(resp.Services, res)
			continue
		}
		resp.Checked = true
		// 本機沒有 RepoDigest(例如本機 build 的映像)就無法比對,視為「未知」。
		if local == "" {
			res.Error = "本机镜像没有可比对的摘要(可能是本地构建的镜像)"
			resp.Services = append(resp.Services, res)
			continue
		}
		if local != remote {
			res.UpdateAvailable = true
			resp.UpdateAvailable = true
		}
		resp.Services = append(resp.Services, res)
	}

	writeJSON(w, http.StatusOK, resp)
}
