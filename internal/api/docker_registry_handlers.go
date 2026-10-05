package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/bng147/gonas/internal/docker"
)

// registryConfigResponse 是 GET/PUT /docker/registry-config 的回應。Applied 代表
// 寫檔後 `systemctl reload docker` 有沒有成功(沿用 share 那套 applied+warning
// 的模式):沒成功時設定已寫進 daemon.json,但要等 Docker 下次 reload/restart
// 才生效,Warning 會說明。
type registryConfigResponse struct {
	RegistryMirrors    []string `json:"registryMirrors"`
	InsecureRegistries []string `json:"insecureRegistries"`
	Applied            bool     `json:"applied"`
	Warning            string   `json:"warning,omitempty"`
}

func (s *Server) handleDockerRegistryConfigGet(w http.ResponseWriter, r *http.Request) {
	view, err := docker.ReadDaemonConfig(s.dockerDaemonJSONPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, registryConfigResponse{
		RegistryMirrors:    view.RegistryMirrors,
		InsecureRegistries: view.InsecureRegistries,
	})
}

type registryConfigSetRequest struct {
	RegistryMirrors    []string `json:"registryMirrors"`
	InsecureRegistries []string `json:"insecureRegistries"`
}

// handleDockerRegistryConfigSet 寫入鏡像加速 / insecure registry 設定到
// daemon.json,然後用 `systemctl reload docker`(SIGHUP)熱套用 —— 這兩個欄位
// 都是 dockerd 支援 reload 的,不需要 restart、不會中斷正在跑的容器。requireAdmin。
func (s *Server) handleDockerRegistryConfigSet(w http.ResponseWriter, r *http.Request) {
	var req registryConfigSetRequest
	if !readJSON(w, r, &req) {
		return
	}

	// 清理 + 驗證每一條。
	mirrors := make([]string, 0, len(req.RegistryMirrors))
	seenM := make(map[string]bool)
	for _, m := range req.RegistryMirrors {
		m = strings.TrimSpace(m)
		if m == "" {
			continue
		}
		if err := docker.ValidateRegistryMirror(m); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if !seenM[m] {
			seenM[m] = true
			mirrors = append(mirrors, m)
		}
	}
	insecure := make([]string, 0, len(req.InsecureRegistries))
	seenI := make(map[string]bool)
	for _, reg := range req.InsecureRegistries {
		reg = strings.TrimSpace(reg)
		if reg == "" {
			continue
		}
		if err := docker.ValidateInsecureRegistry(reg); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if !seenI[reg] {
			seenI[reg] = true
			insecure = append(insecure, reg)
		}
	}

	if err := docker.WriteDaemonConfig(s.dockerDaemonJSONPath, docker.DaemonConfigView{
		RegistryMirrors:    mirrors,
		InsecureRegistries: insecure,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	resp := registryConfigResponse{RegistryMirrors: mirrors, InsecureRegistries: insecure}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if out, err := s.runner.Run(ctx, "systemctl", "reload", "docker"); err != nil {
		s.logger.Warn("reloading docker after registry-config change failed", "err", err, "out", string(out))
		resp.Applied = false
		resp.Warning = "设置已写入 daemon.json,但 systemctl reload docker 失败(Docker 可能未安装/未运行)。下次 Docker 启动时会生效。"
	} else {
		resp.Applied = true
	}
	writeJSON(w, http.StatusOK, resp)
}
