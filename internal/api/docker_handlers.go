package api

import (
	"net/http"
	"strings"

	"github.com/bng147/gonas/internal/docker"
)

// dockerStatusResponse 讓 Web UI 能區分「Docker 沒裝/沒啟動」跟其他錯誤,
// 這是安裝任何 App 之前第一件要確認的事。
type dockerStatusResponse struct {
	Available bool   `json:"available"`
	Error     string `json:"error,omitempty"`
}

func (s *Server) handleDockerPing(w http.ResponseWriter, r *http.Request) {
	if err := s.docker.Ping(r.Context()); err != nil {
		writeJSON(w, http.StatusOK, dockerStatusResponse{Available: false, Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, dockerStatusResponse{Available: true})
}

func (s *Server) handleDockerContainers(w http.ResponseWriter, r *http.Request) {
	containers, err := s.docker.ListContainers(r.Context(), true)
	if err != nil {
		s.logger.Error("listing docker containers failed", "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, containers)
}

func (s *Server) handleDockerImages(w http.ResponseWriter, r *http.Request) {
	images, err := s.docker.ListImages(r.Context())
	if err != nil {
		s.logger.Error("listing docker images failed", "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, images)
}

func (s *Server) handleDockerNetworks(w http.ResponseWriter, r *http.Request) {
	networks, err := s.docker.ListNetworks(r.Context())
	if err != nil {
		s.logger.Error("listing docker networks failed", "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, networks)
}

// containerLogsResponse 是 GET .../containers/{id}/logs 的回應,已經把
// Docker Engine API 的多工串流格式解開成一份純文字。
type containerLogsResponse struct {
	Logs string `json:"logs"`
}

// handleContainerLogs 對應「應用程式」頁面上每個容器的「查看 log」按鈕
// ——診斷一個容器為什麼一直重啟、有沒有印出錯誤訊息,不用再叫使用者自己
// SSH 進機器打 `docker logs`。tail 查詢參數留空預設抓全部,對長期執行的
// 容器(例如資料庫)建議前端固定帶一個上限(例如 500 行),避免一次
// 抓回過大的內容。
func (s *Server) handleContainerLogs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	tail := strings.TrimSpace(r.URL.Query().Get("tail"))
	logs, err := s.docker.ContainerLogs(r.Context(), id, docker.ContainerLogsOptions{Tail: tail, Timestamps: true})
	if err != nil {
		s.logger.Error("fetching container logs failed", "err", err, "containerId", id)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, containerLogsResponse{Logs: logs})
}

// containerExecRequest 是 POST .../containers/{id}/exec 的請求 body:一個
// 要在容器裡執行一次的指令,例如 {"cmd": ["sh", "-c", "cat /etc/os-release"]}。
type containerExecRequest struct {
	Cmd []string `json:"cmd"`
}

type containerExecResponse struct {
	Output   string `json:"output"`
	ExitCode int    `json:"exitCode"`
}

// handleContainerExec 對應「應用程式」頁面上的「執行指令」功能,等同
// `docker exec <container> <cmd...>` 但只等指令跑完一次就回傳完整輸出,
// 不是持續連線的互動式終端機(理由見 internal/docker/exec.go 的說明)。
// 對「這個容器裡到底裝了什麼、設定檔長怎樣」這類一次性診斷已經足夠。
func (s *Server) handleContainerExec(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req containerExecRequest
	if !readJSON(w, r, &req) {
		return
	}
	result, err := s.docker.ExecInContainer(r.Context(), id, req.Cmd)
	if err != nil {
		if err == docker.ErrExecEmptyCommand {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		s.logger.Error("exec in container failed", "err", err, "containerId", id)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, containerExecResponse{Output: result.Output, ExitCode: result.ExitCode})
}
