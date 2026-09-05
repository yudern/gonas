package wireguard

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"
)

// PeerConfig 是一個 WireGuard 對端(通常是使用者的手機、筆電這類要
// 從外部連回 NAS 的裝置)的設定。
type PeerConfig struct {
	// ID 是 GoNAS 自己配的識別碼(見 internal/api.newID),純粹給 REST API
	// 當資源路徑用(DELETE /api/v1/vpn/peers/{id})——公鑰本身是 base64,
	// 含有 `/` `+` `=` 這些在 URL path segment 裡麻煩的字元,不適合直接
	// 拿來當路徑參數,這點跟 AlertRule/WebhookConfig 用 newID 當 ID 是
	// 同樣的考量。不會出現在 wg-quick 讀取的欄位裡。
	ID                  string   `json:"id,omitempty"`
	Name                string   `json:"name"` // 純粹給人看的識別名稱,不會出現在 wg-quick 讀取的欄位裡,只當註解用
	PublicKey           string   `json:"publicKey"`
	PresharedKey        string   `json:"presharedKey,omitempty"`
	AllowedIPs          []string `json:"allowedIPs"`
	Endpoint            string   `json:"endpoint,omitempty"`
	PersistentKeepalive int      `json:"persistentKeepalive,omitempty"`
}

// Validate 檢查單一 peer 的設定是否完整。
func (p PeerConfig) Validate() error {
	if p.Name == "" {
		return fmt.Errorf("peer name is required")
	}
	if p.PublicKey == "" {
		return fmt.Errorf("peer %q: public key is required", p.Name)
	}
	if len(p.AllowedIPs) == 0 {
		return fmt.Errorf("peer %q: at least one allowed IP/CIDR is required", p.Name)
	}
	return nil
}

// InterfaceConfig 是 GoNAS 自己這台機器上 WireGuard 介面的設定。
type InterfaceConfig struct {
	PrivateKey string   `json:"privateKey"`
	Address    []string `json:"address"`
	ListenPort int      `json:"listenPort"`
}

// Validate 檢查介面設定是否完整、埠號是否落在合法範圍。
func (i InterfaceConfig) Validate() error {
	if i.PrivateKey == "" {
		return fmt.Errorf("interface private key is required")
	}
	if len(i.Address) == 0 {
		return fmt.Errorf("interface address (CIDR) is required")
	}
	if i.ListenPort <= 0 || i.ListenPort > 65535 {
		return fmt.Errorf("interface listen port must be between 1 and 65535, got %d", i.ListenPort)
	}
	return nil
}

// Config 是一個完整的 WireGuard 介面設定:自己這端的介面設定加上所有
// 允許連進來的對端。
type Config struct {
	Interface InterfaceConfig `json:"interface"`
	Peers     []PeerConfig    `json:"peers"`
}

// Validate 檢查整份設定,包含每個 peer 各自的合法性、以及 peer 之間
// 不能重複用同一把公鑰(重複的話 wg-quick 實際套用時的行為是未定義的,
// 不該讓這種設定寫得進去)。
func (c Config) Validate() error {
	if err := c.Interface.Validate(); err != nil {
		return err
	}
	seen := make(map[string]bool, len(c.Peers))
	for _, p := range c.Peers {
		if err := p.Validate(); err != nil {
			return err
		}
		if seen[p.PublicKey] {
			return fmt.Errorf("duplicate peer public key %q (peer %q)", p.PublicKey, p.Name)
		}
		seen[p.PublicKey] = true
	}
	return nil
}

// configTemplate 產生標準的 wg-quick .conf 格式。跟 internal/storage 的
// SnapRAID 設定檔、internal/share 的 Samba/NFS 設定檔用的是同一套
// text/template 手法。
var configTemplate = template.Must(template.New("wireguard-conf").
	Funcs(template.FuncMap{"join": strings.Join}).
	Parse(`# 由 GoNAS 產生,手動編輯這個檔案的變更會在下次套用設定時被覆蓋。
[Interface]
PrivateKey = {{.Interface.PrivateKey}}
Address = {{join .Interface.Address ", "}}
ListenPort = {{.Interface.ListenPort}}
{{range .Peers}}
[Peer]
# {{.Name}}
PublicKey = {{.PublicKey}}
{{- if .PresharedKey}}
PresharedKey = {{.PresharedKey}}
{{- end}}
AllowedIPs = {{join .AllowedIPs ", "}}
{{- if .Endpoint}}
Endpoint = {{.Endpoint}}
{{- end}}
{{- if .PersistentKeepalive}}
PersistentKeepalive = {{.PersistentKeepalive}}
{{- end}}
{{end}}`))

// GenerateConfig 把 Config 轉成 wg-quick 讀得懂的 .conf 文字內容。
func GenerateConfig(cfg Config) (string, error) {
	if err := cfg.Validate(); err != nil {
		return "", fmt.Errorf("invalid wireguard config: %w", err)
	}
	var buf bytes.Buffer
	if err := configTemplate.Execute(&buf, cfg); err != nil {
		return "", fmt.Errorf("rendering wireguard config: %w", err)
	}
	return buf.String(), nil
}
