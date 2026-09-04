package api

import "net/http"

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
