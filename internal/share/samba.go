// Package share 是 GoNAS 的檔案共享與帳號層：產生 Samba(SMB)/NFS 設定、
// 熱重載,以及對應的本機使用者/群組管理與密碼同步。
//
// 設計上刻意不去改寫系統既有的 /etc/samba/smb.conf 或 /etc/exports 本身,
// 而是各自寫一份 GoNAS 自己管理的 include 檔案(/etc/samba/gonas-shares.conf、
// /etc/exports.d/gonas.exports),原因跟 storage 套件的 SnapRAID 設定檔一樣：
// 這個檔案「由 GoNAS 自動產生,請勿手動修改」,人工在系統原生設定檔上做的
// 客製化(例如印表機共享、VPN 相關的 Samba 設定)不會被我們的產生器覆蓋掉。
package share

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/bng147/gonas/internal/cmdrunner"
	"github.com/bng147/gonas/internal/textcheck"
)

// Share 描述一個 SMB 共享。Path 應該指向 mergerFS 聯合掛載點底下的一個
// 子目錄(例如 "/mnt/tank/media"),而不是直接共享整個陣列根目錄 —— 讓
// 使用者可以針對不同用途(媒體庫、備份、Docker appdata)分別設定權限。
type Share struct {
	Name       string   `json:"name"` // Samba 共享名稱，使用者在網路上看到的名字
	Path       string   `json:"path"` // 主機上的實際路徑
	Comment    string   `json:"comment,omitempty"`
	ReadOnly   bool     `json:"readOnly"`
	GuestOK    bool     `json:"guestOk"`              // 允許匿名存取；預設應為 false，安裝精靈要讓使用者明確勾選
	ValidUsers []string `json:"validUsers,omitempty"` // 空代表沿用 [global] 的存取控制
}

func (s Share) Validate() error {
	if s.Name == "" {
		return fmt.Errorf("share: name is required")
	}
	if s.Path == "" {
		return fmt.Errorf("share %q: path is required", s.Name)
	}
	if strings.ContainsAny(s.Name, `[]"`) {
		return fmt.Errorf("share %q: name cannot contain '[', ']' or '\"' (breaks smb.conf syntax)", s.Name)
	}
	// 第三十三輪(全鏈路第二輪覆核):smb.conf 是「一行一個設定」的格式,
	// 而這個檔案是用 text/template 產生的——text/template 不會跳脫換行,
	// 所以任何欄位裡的換行/控制字元都會直接寫進檔案,破壞格式、甚至注入
	// 額外的設定指令(例如在 Path 裡塞一個換行加 "guest ok = yes")。
	// 建共享目前是 requireAdmin,威脅有限,但這是應該擋的縱深防禦 + 正確性
	// 問題,所以在產生設定檔之前就把含控制字元的值擋下來。
	if hasControlChars(s.Name) {
		return fmt.Errorf("share %q: name cannot contain control characters or line breaks", s.Name)
	}
	if hasControlChars(s.Path) {
		return fmt.Errorf("share %q: path cannot contain control characters or line breaks", s.Name)
	}
	if hasControlChars(s.Comment) {
		return fmt.Errorf("share %q: comment cannot contain control characters or line breaks", s.Name)
	}
	for _, u := range s.ValidUsers {
		// 換行/控制字元會破壞格式;逗號是 "valid users = a, b" 的分隔符,
		// 一個含逗號的「使用者名稱」會被 smbd 當成多個使用者(注入)。
		if hasControlChars(u) || strings.Contains(u, ",") {
			return fmt.Errorf("share %q: valid-user entry %q cannot contain a comma, line break, or control character", s.Name, u)
		}
	}
	return nil
}

// hasControlChars 是 textcheck.HasControl 的薄包裝,保留 samba/nfs 既有
// 呼叫點的名稱不變;實作已收斂到 internal/textcheck 讓各設定產生器共用。
func hasControlChars(s string) bool {
	return textcheck.HasControl(s)
}

const sambaConfTemplate = `# 由 GoNAS 自動產生，請勿手動修改 —— 修改請透過 Web UI 或 API。
# 這個檔案應該被系統的 /etc/samba/smb.conf 用 "include = {{.IncludePathHint}}" 引入,
# 而不是取代它，這樣使用者原本在 smb.conf 裡的其他設定不會被蓋掉。
{{range .Shares}}
[{{.Name}}]
	path = {{.Path}}
	comment = {{.Comment}}
	read only = {{if .ReadOnly}}yes{{else}}no{{end}}
	guest ok = {{if .GuestOK}}yes{{else}}no{{end}}
	browseable = yes
{{- if .ValidUsers}}
	valid users = {{join .ValidUsers ", "}}
{{- end}}
{{end}}`

type sambaConfData struct {
	Shares          []Share
	IncludePathHint string
}

var sambaTmpl = template.Must(template.New("smb-shares.conf").Funcs(template.FuncMap{
	"join": strings.Join,
}).Parse(sambaConfTemplate))

// GenerateSambaConfig 依共享清單產生 Samba include 檔案內容。
func GenerateSambaConfig(shares []Share, includePathHint string) (string, error) {
	for _, s := range shares {
		if err := s.Validate(); err != nil {
			return "", fmt.Errorf("refusing to generate samba config: %w", err)
		}
	}
	seen := make(map[string]bool, len(shares))
	for _, s := range shares {
		if seen[s.Name] {
			return "", fmt.Errorf("refusing to generate samba config: duplicate share name %q", s.Name)
		}
		seen[s.Name] = true
	}

	var buf bytes.Buffer
	if err := sambaTmpl.Execute(&buf, sambaConfData{Shares: shares, IncludePathHint: includePathHint}); err != nil {
		return "", fmt.Errorf("rendering samba config: %w", err)
	}
	return buf.String(), nil
}

// WriteConfigAtomically 把內容寫進 path，先寫到同目錄下的暫存檔再 rename,
// 確保不會有「寫到一半被中斷、留下半份設定檔」的情況 —— rename 在同一個
// 檔案系統內是原子操作。Samba/NFS/SnapRAID 的設定檔寫入都共用這個函式。
func WriteConfigAtomically(path, content string) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".gonas-tmp-*")
	if err != nil {
		return fmt.Errorf("creating temp file in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // rename 成功後這行會是 no-op(檔案已經不在原路徑)

	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return fmt.Errorf("writing %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", tmpPath, err)
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		return fmt.Errorf("chmod %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("renaming %s to %s: %w", tmpPath, path, err)
	}
	return nil
}

// ValidateSambaConfig 用 testparm 檢查設定檔語法是否正確。刻意在 ReloadSamba
// 之前呼叫這個 —— 寧可拒絕套用一份有問題的設定,也不要讓 smbd 因為設定檔
// 壞掉而整個掛掉,那樣會讓所有共享同時斷線,而不只是新加的那個。
func ValidateSambaConfig(ctx context.Context, r cmdrunner.Runner, path string) error {
	out, err := r.Run(ctx, "testparm", "-s", path)
	if err != nil {
		return fmt.Errorf("samba config at %s failed validation: %w (output: %s)", path, err, string(out))
	}
	return nil
}

// ReloadSamba 通知已經在跑的 smbd 重新讀取設定，不需要整個服務重啟
// (重啟會踢掉所有正在連線的使用者)。
func ReloadSamba(ctx context.Context, r cmdrunner.Runner) error {
	if _, err := r.Run(ctx, "smbcontrol", "smbd", "reload-config"); err != nil {
		return fmt.Errorf("reloading samba config: %w", err)
	}
	return nil
}

// SyncSambaPassword 幫一個系統帳號設定/更新 Samba 密碼(smbpasswd 資料庫跟
// 系統的 /etc/shadow 是分開的兩份密碼儲存，Windows 用戶端走 SMB 認證時查的
// 是前者，所以建立使用者、改密碼都要同時處理這兩邊，不能只改系統密碼)。
// -s 讓 smbpasswd 從 stdin 讀密碼兩次，而不是留在 shell 指令列的參數裡。
func SyncSambaPassword(ctx context.Context, r cmdrunner.Runner, username, password string) error {
	stdin := []byte(password + "\n" + password + "\n")
	if _, err := r.RunWithStdin(ctx, stdin, "smbpasswd", "-s", "-a", username); err != nil {
		return fmt.Errorf("syncing samba password for %q: %w", username, err)
	}
	return nil
}
