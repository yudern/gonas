package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/bng147/gonas/internal/timekeep"
)

// 第七十五輪:系統時間/時區/NTP。見 internal/timekeep。

// GET /api/v1/system/time —— 目前狀態 + 可選時區清單。
func (s *Server) handleTimeGet(w http.ResponseWriter, r *http.Request) {
	status := timekeep.GetStatus(r.Context(), s.runner)
	zones := timekeep.ListTimezones(r.Context(), s.runner)
	writeJSON(w, http.StatusOK, map[string]any{
		"status":    status,
		"timezones": zones,
	})
}

// PUT /api/v1/system/time/timezone  { "timezone": "Asia/Shanghai" }
func (s *Server) handleTimeSetTimezone(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Timezone string `json:"timezone"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("parsing request: %w", err))
		return
	}
	if err := timekeep.SetTimezone(r.Context(), s.runner, body.Timezone); err != nil {
		s.logger.Warn("set timezone failed", "err", err, "tz", body.Timezone)
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s.logger.Info("timezone changed", "tz", body.Timezone)
	writeJSON(w, http.StatusOK, timekeep.GetStatus(r.Context(), s.runner))
}

// PUT /api/v1/system/time/ntp  { "enabled": true }
func (s *Server) handleTimeSetNTP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("parsing request: %w", err))
		return
	}
	if err := timekeep.SetNTP(r.Context(), s.runner, body.Enabled); err != nil {
		s.logger.Warn("set ntp failed", "err", err)
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s.logger.Info("ntp setting changed", "enabled", body.Enabled)
	writeJSON(w, http.StatusOK, timekeep.GetStatus(r.Context(), s.runner))
}

// PUT /api/v1/system/time/manual  { "time": "2026-10-08T23:45" }
// 手動設定時間;datetime-local 送來的字串以本地時區解讀。開著 NTP 時
// timedatectl 會拒絕,錯誤原樣回報給使用者。
func (s *Server) handleTimeSetManual(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Time string `json:"time"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("parsing request: %w", err))
		return
	}
	t, err := time.ParseInLocation("2006-01-02T15:04", body.Time, time.Local)
	if err != nil {
		// 也接受帶秒的格式。
		t, err = time.ParseInLocation("2006-01-02T15:04:05", body.Time, time.Local)
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("parsing time %q: %w", body.Time, err))
		return
	}
	if err := timekeep.SetTime(r.Context(), s.runner, t); err != nil {
		s.logger.Warn("set time failed", "err", err)
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s.logger.Info("system time set manually")
	writeJSON(w, http.StatusOK, timekeep.GetStatus(r.Context(), s.runner))
}
