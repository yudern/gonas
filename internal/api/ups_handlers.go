package api

import (
	"errors"
	"net/http"

	"github.com/bng147/gonas/internal/state"
	"github.com/bng147/gonas/internal/ups"
)

// handleUPSStatus 回傳目前 UPS 的即時狀態。查不到(NUT 沒裝、沒設定 UPS、
// upsc 失敗)一律回 200 + Present:false + 說明,不回錯誤——「沒有 UPS」是
// 正常狀態、不是伺服器錯誤,前端據 present 顯示對應畫面。
func (s *Server) handleUPSStatus(w http.ResponseWriter, r *http.Request) {
	name := s.store.Snapshot().UPS.UPSName
	if name == "" {
		// 沒指定就試著自動抓 NUT 裡設定的第一台。
		if names, err := ups.List(r.Context(), s.runner); err == nil && len(names) > 0 {
			name = names[0]
		}
	}
	if name == "" {
		writeJSON(w, http.StatusOK, ups.Status{Present: false, Message: "no UPS is configured in NUT (or NUT is not installed)"})
		return
	}
	st, err := ups.Query(r.Context(), s.runner, name)
	if err != nil {
		writeJSON(w, http.StatusOK, ups.Status{Present: false, Message: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// handleUPSList 回傳 NUT 目前設定了哪些 UPS 名稱(給設定畫面的下拉選單)。
// 失敗(NUT 沒裝等)回空陣列而不是錯誤。
func (s *Server) handleUPSList(w http.ResponseWriter, r *http.Request) {
	names, err := ups.List(r.Context(), s.runner)
	if err != nil {
		writeJSON(w, http.StatusOK, []string{})
		return
	}
	writeJSON(w, http.StatusOK, names)
}

func (s *Server) handleUPSConfigGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.store.Snapshot().UPS)
}

// handleUPSConfigSet 更新 UPS 設定(requireAdmin)。背景監控每一輪都重讀
// 目前設定(見 New() 裡 upsMonitor 的 getConfig),所以這裡改完存檔即可生效,
// 不需要重啟監控。
func (s *Server) handleUPSConfigSet(w http.ResponseWriter, r *http.Request) {
	var cfg state.UPSConfig
	if !readJSON(w, r, &cfg) {
		return
	}
	if cfg.RuntimeThresholdSeconds < 0 {
		writeError(w, http.StatusBadRequest, errors.New("the runtime threshold cannot be negative"))
		return
	}
	if err := s.store.Update(func(st *state.State) error {
		st.UPS = cfg
		return nil
	}); err != nil {
		s.logger.Error("persisting UPS config failed", "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, cfg)
}
