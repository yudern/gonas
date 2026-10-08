package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/bng147/gonas/internal/configsync"
	"github.com/bng147/gonas/internal/version"
)

// 第七十五輪:設定匯出/匯入(重灌/搬機保留大部分設定)。見 internal/configsync。

// GET /api/v1/system/config/export —— 下載一份已脫敏的設定匯出 JSON。
func (s *Server) handleConfigExport(w http.ResponseWriter, r *http.Request) {
	hostname, _ := os.Hostname()
	ex := configsync.Export(s.store.Snapshot(), version.Version, hostname)
	body, err := json.MarshalIndent(ex, "", "  ")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	fname := fmt.Sprintf("gonas-config-%s.json", time.Now().Format("2006-01-02"))
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", fname))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// POST /api/v1/system/config/import —— 套用選定區塊。
// 請求 body:{ "file": <匯出檔物件>, "sections": {accounts,notifications,systemConfig} }
func (s *Server) handleConfigImport(w http.ResponseWriter, r *http.Request) {
	var body struct {
		File     configsync.ExportFile `json:"file"`
		Sections configsync.Sections   `json:"sections"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("parsing import: %w", err))
		return
	}
	rep, err := configsync.Apply(s.store, body.File, body.Sections)
	if err != nil {
		s.logger.Warn("config import failed", "err", err)
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s.logger.Info("config imported", "restored", rep.Restored)
	writeJSON(w, http.StatusOK, rep)
}
