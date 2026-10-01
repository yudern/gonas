package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

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
// maxContainerLogTail 是 log 端點願意回傳的最大行數。第六十輪 QA 覆核:
// tail 留空時 docker 會回「全部」,demuxStream 又把整段讀進記憶體,對一個
// 話很多的長命容器直接呼叫(不帶 tail)可能把記憶體撐爆。前端一向帶
// tail=200,但 API 不能只靠前端自律——這裡把 tail 夾在一個上限內,空值或
// 超過上限都收斂成 maxContainerLogTail。
const maxContainerLogTail = 2000

func (s *Server) handleContainerLogs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	tail := strings.TrimSpace(r.URL.Query().Get("tail"))
	// 夾住 tail:空值、非數字、超過上限,一律用上限,避免無界讀取。
	if n, err := strconv.Atoi(tail); err != nil || n <= 0 || n > maxContainerLogTail {
		tail = strconv.Itoa(maxContainerLogTail)
	}
	logs, err := s.docker.ContainerLogs(r.Context(), id, docker.ContainerLogsOptions{Tail: tail, Timestamps: true})
	if err != nil {
		s.logger.Error("fetching container logs failed", "err", err, "containerId", id)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, containerLogsResponse{Logs: logs})
}

// containerLifecycleStopTimeoutSec 是 stop/restart 給容器優雅關閉的秒數,
// 超過就強制 kill。10 秒是 docker 的預設值,對絕大多數容器都夠。
const containerLifecycleStopTimeoutSec = 10

// handleContainerStart / handleContainerStop / handleContainerRestart 是
// 「應用程式」頁面每個容器的啟動/停止/重啟按鈕(第六十輪使用者需求:原本
// 只有解除安裝/看 log/執行指令,少了最基本的開關)。三支都是 requireAdmin
// (見 router.go)——啟停容器是管理動作。刻意用「跟 HTTP 請求脫鉤」不做,
// 這些操作很快(幾秒內),直接用 r.Context() 即可。
func (s *Server) handleContainerStart(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.docker.StartContainer(r.Context(), id); err != nil {
		s.logger.Error("starting container failed", "err", err, "containerId", id)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "started"})
}

func (s *Server) handleContainerStop(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.docker.StopContainer(r.Context(), id, containerLifecycleStopTimeoutSec); err != nil {
		s.logger.Error("stopping container failed", "err", err, "containerId", id)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "stopped"})
}

func (s *Server) handleContainerRestart(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.docker.RestartContainer(r.Context(), id, containerLifecycleStopTimeoutSec); err != nil {
		s.logger.Error("restarting container failed", "err", err, "containerId", id)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "restarted"})
}

// handleDockerImageRemove 刪除一個映像(第六十輪產品覆核:映像會越積越多、
// 悄悄塞滿系統碟,卻沒有清理的出口)。requireAdmin。被執行中容器使用的映像
// Docker 會回 409,錯誤會透傳給前端顯示。
func (s *Server) handleDockerImageRemove(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.docker.RemoveImage(r.Context(), id, false); err != nil {
		s.logger.Warn("removing image failed", "err", err, "image", id)
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "removed"})
}

// handleDockerImagesPrune 清掉所有懸空(未使用、無 tag)映像。requireAdmin。
func (s *Server) handleDockerImagesPrune(w http.ResponseWriter, r *http.Request) {
	res, err := s.docker.PruneImages(r.Context())
	if err != nil {
		s.logger.Error("pruning images failed", "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleContainerStats 回傳單一容器一次性的資源用量(CPU%/記憶體),給
// 「應用程式」頁面顯示即時負載。requireAuth 即可(唯讀)。
func (s *Server) handleContainerStats(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	// 統計會等 daemon 取兩個樣點,給一個獨立的短逾時,避免掛在請求上太久。
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	stats, err := s.docker.ContainerStats(ctx, id)
	if err != nil {
		s.logger.Error("getting container stats failed", "err", err, "containerId", id)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

// handleContainerRemove 刪除一個容器(給「所有容器」清單裡管理非 GoNAS 建立的
// 獨立容器用;GoNAS 自己裝的 App 請走「解除安裝」以連同網路一起清掉)。
// requireAdmin。預設會先停再刪(force)。
func (s *Server) handleContainerRemove(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.docker.RemoveContainer(r.Context(), id, true); err != nil {
		s.logger.Error("removing container failed", "err", err, "containerId", id)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "removed"})
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
