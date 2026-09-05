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
//
// httpsCertRenewBefore 是背景續期的門檻:憑證剩餘效期進入這個門檻內就
// 會自動重簽一份新的(見 internal/security.CertRenewer)。30 天給了
// 足夠的緩衝——即使 gonasd 因為某些原因停機一段時間沒能即時續期,
// 通常也不會真的撐到憑證過期那一刻才發現。
//
// httpsCertRenewCheckInterval 是背景續期檢查的頻率。憑證效期是以年為
// 單位在動,不需要頻繁檢查;24 小時一次已經遠遠夠用,又不會造成任何
// 有意義的額外負擔。
const (
	httpsCertValidity           = 2 * 365 * 24 * time.Hour
	httpsCertRenewBefore        = 30 * 24 * time.Hour
	httpsCertRenewCheckInterval = 24 * time.Hour
)

// httpsDefaultCertPath / httpsDefaultKeyPath 是憑證檔案的預設位置,放在
// dataDir 底下(跟 state.json 同一層的 tls/ 子目錄),避免散落在系統其他
// 目錄裡、之後找不到或跟其他服務的憑證搞混。
func (s *Server) httpsCertPath() string { return s.dataDir + "/tls/cert.pem" }
func (s *Server) httpsKeyPath() string  { return s.dataDir + "/tls/key.pem" }

type httpsSettingsResponse struct {
	Enabled  bool     `json:"enabled"`
	CertPath string   `json:"certPath,omitempty"`
	KeyPath  string   `json:"keyPath,omitempty"`
	Hosts    []string `json:"hosts,omitempty"`
	// CertExpiresAt 是現有憑證檔案的到期時間(RFC 3339),讀不到憑證
	// (檔案不存在、還沒開過 HTTPS)時留空。純粹是給 Web UI 顯示用的
	// 唯讀資訊——真正決定要不要續期的邏輯在
	// internal/security.RenewCertIfNeeded,不是這裡。
	CertExpiresAt         string `json:"certExpiresAt,omitempty"`
	RestartRequiredNotice string `json:"restartRequiredNotice"`
}

// httpsCertExpiryOrEmpty 讀 certPath 這份憑證的到期時間,讀不到(檔案
// 不存在、剖析失敗)就回傳空字串——這只是給前端顯示用的輔助資訊,不該
// 因為讀不到就讓整支 GET /PUT 端點回 500。
func httpsCertExpiryOrEmpty(certPath string) string {
	if certPath == "" {
		return ""
	}
	expiry, err := security.LoadCertExpiry(certPath)
	if err != nil {
		return ""
	}
	return expiry.UTC().Format(time.RFC3339)
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
		Hosts:                 cfg.Hosts,
		CertExpiresAt:         httpsCertExpiryOrEmpty(cfg.CertPath),
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
		st.HTTPS = state.HTTPSConfig{Enabled: req.Enabled, CertPath: certPath, KeyPath: keyPath, Hosts: req.Hosts}
		return nil
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	writeJSON(w, http.StatusOK, httpsSettingsResponse{
		Enabled:               req.Enabled,
		CertPath:              certPath,
		KeyPath:               keyPath,
		Hosts:                 req.Hosts,
		CertExpiresAt:         httpsCertExpiryOrEmpty(certPath),
		RestartRequiredNotice: restartRequiredNotice,
	})
}
