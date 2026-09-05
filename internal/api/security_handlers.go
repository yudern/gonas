package api

import (
	"net/http"
	"time"

	"github.com/bng147/gonas/internal/security"
	"github.com/bng147/gonas/internal/state"
)

// httpsCertValidity 是自簽憑證的有效期。自簽憑證反正不會被公開信任的
// CA 鏈驗證,使用者一律要手動信任一次,拉長效期單純是為了不用每年
// 提醒使用者「憑證要過期了、記得重新信任」——2 年是在「夠長不擾民」跟
// 「私鑰萬一外洩,舊憑證不會永遠有效」之間一個合理的折衷。
const httpsCertValidity = 2 * 365 * 24 * time.Hour

// httpsDefaultCertPath / httpsDefaultKeyPath 是憑證檔案的預設位置,放在
// dataDir 底下(跟 state.json 同一層的 tls/ 子目錄),避免散落在系統其他
// 目錄裡、之後找不到或跟其他服務的憑證搞混。
func (s *Server) httpsCertPath() string { return s.dataDir + "/tls/cert.pem" }
func (s *Server) httpsKeyPath() string  { return s.dataDir + "/tls/key.pem" }

type httpsSettingsResponse struct {
	Enabled               bool   `json:"enabled"`
	CertPath              string `json:"certPath,omitempty"`
	KeyPath               string `json:"keyPath,omitempty"`
	RestartRequiredNotice string `json:"restartRequiredNotice"`
}

const restartRequiredNotice = "變更 HTTPS 設定不會立刻生效：gonasd 只在程序啟動時決定要監聽 HTTP 還是 HTTPS,請重新啟動 gonasd 讓新設定生效。"

// handleSecurityHTTPSGet 回傳目前的 HTTPS 設定。刻意不回傳金鑰檔案內容
// 本身(KeyPath 只是路徑字串,不是私鑰內容),但仍然限制在 requireAuth
// 之後才能呼叫,避免路徑資訊(可能間接透露主機檔案系統佈局)被匿名讀取。
func (s *Server) handleSecurityHTTPSGet(w http.ResponseWriter, r *http.Request) {
	cfg := s.store.Snapshot().HTTPS
	writeJSON(w, http.StatusOK, httpsSettingsResponse{
		Enabled:               cfg.Enabled,
		CertPath:              cfg.CertPath,
		KeyPath:               cfg.KeyPath,
		RestartRequiredNotice: restartRequiredNotice,
	})
}

type httpsSettingsRequest struct {
	Enabled bool     `json:"enabled"`
	Hosts   []string `json:"hosts,omitempty"` // 憑證 SAN 用的主機名稱/IP,例如使用者的區網 IP 或 DDNS 網域;留空時退回 localhost/127.0.0.1
}

// handleSecurityHTTPSSet 開啟 HTTPS 時,如果憑證檔案還不存在就順手產生
// 一份自簽憑證(EnsureCertFiles 本身是 idempotent 的,已經存在就不重簽)
// ——這樣使用者只要按下「開啟 HTTPS」就好,不需要自己先跑一個獨立的
// 「產生憑證」步驟。關閉 HTTPS 只是把 Enabled 記錄成 false,憑證檔案
// 留著不刪,下次重新開啟不用再簽一次。
func (s *Server) handleSecurityHTTPSSet(w http.ResponseWriter, r *http.Request) {
	var req httpsSettingsRequest
	if !readJSON(w, r, &req) {
		return
	}

	certPath, keyPath := s.httpsCertPath(), s.httpsKeyPath()
	if req.Enabled {
		if err := security.EnsureCertFiles(certPath, keyPath, req.Hosts, httpsCertValidity); err != nil {
			s.logger.Error("ensuring tls certificate files failed", "err", err)
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	}

	if err := s.store.Update(func(st *state.State) error {
		st.HTTPS = state.HTTPSConfig{Enabled: req.Enabled, CertPath: certPath, KeyPath: keyPath}
		return nil
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	writeJSON(w, http.StatusOK, httpsSettingsResponse{
		Enabled:               req.Enabled,
		CertPath:              certPath,
		KeyPath:               keyPath,
		RestartRequiredNotice: restartRequiredNotice,
	})
}
