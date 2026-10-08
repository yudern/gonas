// Package configsync 匯出/匯入 GoNAS 的設定,方便重灌或搬機時一鍵還原大部分
// 設定(第七十五輪,使用者需求)。
//
// 匯出的是「GoNAS 自己維護的設定」——不是作業系統、也不是你資料盤裡的檔案
// (那些靠備份)。敏感資訊預設不帶明文:雙重驗證密鑰/救援碼、Email 中繼密碼、
// WireGuard 私鑰、App 的密碼類環境變數都會被抹掉;登入密碼只帶「雜湊」
// (不是明文,還原後原本的密碼仍可登入)。
//
// 匯入分兩類:
//   - 可自動還原(純設定、無破壞性):登入帳號、告警規則/通知管道、系統設定
//     (時間排程/更新來源/UPS/HTTPS 意圖/校驗排程/SMART 排程/App 目錄網址)。
//   - 需手動重建(涉及格式化磁碟或得先有陣列/Docker):儲存池、共享(SMB/NFS)、
//     共享使用者、已安裝 App、WireGuard。這些會列在匯入預覽裡提醒你,但不會
//     自動執行有風險的動作。
//
// 只用標準函式庫 + 專案自己的型別。
package configsync

import (
	"fmt"
	"regexp"
	"time"

	"github.com/bng147/gonas/internal/state"
)

// Magic / CurrentVersion 用來辨識這是不是一份 GoNAS 設定匯出檔、以及格式版本。
const (
	Magic          = "gonas-config-export"
	CurrentVersion = 1
)

// ExportFile 是匯出檔的最外層結構。
type ExportFile struct {
	Magic        string      `json:"magic"`
	Version      int         `json:"version"`
	ExportedAt   time.Time   `json:"exportedAt"`
	GonasVersion string      `json:"gonasVersion,omitempty"`
	Hostname     string      `json:"hostname,omitempty"`
	Config       state.State `json:"config"`
}

var secretEnvRe = regexp.MustCompile(`(?i)(pass|secret|token|key|pwd)`)

// Export 從一份 state 快照產生「已脫敏」的匯出檔。傳入的 snap 是呼叫端
// store.Snapshot() 的結果;本函式會在副本上抹掉敏感欄位,不動原本的狀態。
func Export(snap state.State, gonasVersion, hostname string) ExportFile {
	// 稽核紀錄不匯出(隱私 + 對還原無意義)。
	snap.AuditLog = nil

	// 登入帳號:保留雜湊(非明文),抹掉 2FA 密鑰/救援碼、重置 TOTP 計數。
	admins := make([]state.AdminAccount, len(snap.Admins))
	for i, a := range snap.Admins {
		a.TOTPSecret = ""
		a.RecoveryCodes = nil
		a.TOTPEnabled = false
		a.LastTOTPCounter = 0
		admins[i] = a
	}
	snap.Admins = admins

	// Email 通知:抹掉密碼。
	for i := range snap.EmailNotifiers {
		snap.EmailNotifiers[i].Password = ""
	}

	// WireGuard:抹掉私鑰/預共享金鑰(保留結構供參考,還原仍需手動重設)。
	if snap.WireGuard != nil {
		wg := *snap.WireGuard
		wg.Interface.PrivateKey = ""
		for i := range wg.Peers {
			wg.Peers[i].PresharedKey = ""
		}
		snap.WireGuard = &wg
	}

	// 已安裝 App 的 override 環境變數:抹掉密碼類的值。
	for i := range snap.InstalledApps {
		if snap.InstalledApps[i].Overrides == nil {
			continue
		}
		for svc, so := range snap.InstalledApps[i].Overrides {
			if so.Env == nil {
				continue
			}
			for k := range so.Env {
				if secretEnvRe.MatchString(k) {
					so.Env[k] = ""
				}
			}
			snap.InstalledApps[i].Overrides[svc] = so
		}
	}

	return ExportFile{
		Magic:        Magic,
		Version:      CurrentVersion,
		ExportedAt:   time.Now(),
		GonasVersion: gonasVersion,
		Hostname:     hostname,
		Config:       snap,
	}
}

// Sections 是匯入時可選的「要還原哪些可自動還原的區塊」。
type Sections struct {
	Accounts      bool `json:"accounts"`      // 登入帳號(Admins)
	Notifications bool `json:"notifications"` // 告警規則 + webhook/email 通知管道 + 健康摘要
	SystemConfig  bool `json:"systemConfig"`  // 時間排程/更新來源/UPS/HTTPS 意圖/校驗/SMART/App 目錄
}

// Report 回報匯入結果:哪些自動還原了、哪些需要手動重建。
type Report struct {
	Restored []string `json:"restored"` // 已自動還原的區塊(人看的描述由前端 i18n 負責,這裡給 key)
	Manual   []string `json:"manual"`   // 需手動重建的區塊(連同數量)
}

// Validate 檢查一份匯入檔是否合法、可用。
func (f ExportFile) Validate() error {
	if f.Magic != Magic {
		return fmt.Errorf("configsync: not a GoNAS config export (bad magic)")
	}
	if f.Version <= 0 || f.Version > CurrentVersion {
		return fmt.Errorf("configsync: unsupported export version %d", f.Version)
	}
	return nil
}

// Apply 把選定的可自動還原區塊寫回 store。會先驗證。帳號區塊要求匯入檔至少
// 有一個 admin 角色帳號,避免還原後沒有任何人能登入管理介面。回傳一份報告。
func Apply(store *state.Store, f ExportFile, sel Sections) (Report, error) {
	if err := f.Validate(); err != nil {
		return Report{}, err
	}
	var rep Report

	if sel.Accounts {
		hasAdmin := false
		for _, a := range f.Config.Admins {
			if a.Role == state.RoleAdmin && a.PasswordHash != "" {
				hasAdmin = true
				break
			}
		}
		if !hasAdmin {
			return Report{}, fmt.Errorf("configsync: the export has no usable admin account to restore")
		}
	}

	err := store.Update(func(st *state.State) error {
		if sel.Accounts {
			st.Admins = f.Config.Admins
			rep.Restored = append(rep.Restored, "accounts")
		}
		if sel.Notifications {
			st.AlertRules = f.Config.AlertRules
			st.Notifiers = f.Config.Notifiers
			st.EmailNotifiers = f.Config.EmailNotifiers // 密碼已被抹掉,使用者需重填
			st.Digest = f.Config.Digest
			rep.Restored = append(rep.Restored, "notifications")
		}
		if sel.SystemConfig {
			st.Update = f.Config.Update
			st.UPS = f.Config.UPS
			st.ParityScrub = f.Config.ParityScrub
			st.SmartTest = f.Config.SmartTest
			st.AppCatalogURL = f.Config.AppCatalogURL
			// HTTPS:只還原「意圖」(是否啟用 + 主機名),憑證路徑清空讓它下次
			// 重新產生(匯出機器的憑證不該搬到另一台)。
			st.HTTPS.Enabled = f.Config.HTTPS.Enabled
			st.HTTPS.Hosts = f.Config.HTTPS.Hosts
			st.HTTPS.CertPath = ""
			st.HTTPS.KeyPath = ""
			rep.Restored = append(rep.Restored, "systemConfig")
		}
		return nil
	})
	if err != nil {
		return Report{}, err
	}

	// 需手動重建的區塊(列出數量,給使用者知道還有什麼要自己弄)。
	if f.Config.Pool != nil {
		rep.Manual = append(rep.Manual, "pool")
	}
	if n := len(f.Config.Shares); n > 0 {
		rep.Manual = append(rep.Manual, fmt.Sprintf("shares:%d", n))
	}
	if n := len(f.Config.Exports); n > 0 {
		rep.Manual = append(rep.Manual, fmt.Sprintf("exports:%d", n))
	}
	if n := len(f.Config.Users); n > 0 {
		rep.Manual = append(rep.Manual, fmt.Sprintf("shareUsers:%d", n))
	}
	if n := len(f.Config.InstalledApps); n > 0 {
		rep.Manual = append(rep.Manual, fmt.Sprintf("apps:%d", n))
	}
	if f.Config.WireGuard != nil {
		rep.Manual = append(rep.Manual, "wireguard")
	}
	return rep, nil
}
