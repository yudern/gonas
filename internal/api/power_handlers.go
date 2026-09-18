package api

import "net/http"

// handleSystemPowerShutdown / handleSystemPowerReboot 讓管理者從網頁關機或
// 重開這台 NAS。兩支都是 requireAdmin(見 router.go 路由註冊),實際動作交給
// systemctl。指令送出後系統就會開始關機/重開,這個 HTTP 回應能不能完整送達
// 不保證(連線會隨系統關閉而中斷),所以前端不強依賴回應內容——送出成功就
// 顯示「指令已送出」。破壞性/中斷性操作,前端另有「打字確認」把關。
func (s *Server) handleSystemPowerShutdown(w http.ResponseWriter, r *http.Request) {
	s.runPowerAction(w, r, "poweroff")
}

func (s *Server) handleSystemPowerReboot(w http.ResponseWriter, r *http.Request) {
	s.runPowerAction(w, r, "reboot")
}

func (s *Server) runPowerAction(w http.ResponseWriter, r *http.Request, action string) {
	s.logger.Warn("system power action requested via web UI", "action", action)
	if _, err := s.runner.Run(r.Context(), "systemctl", action); err != nil {
		s.logger.Error("system power action failed", "action", action, "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"action": action, "status": "scheduled"})
}
