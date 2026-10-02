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

	// WriteList / ReadList 是「逐使用者」的讀寫權限覆寫,對應 smb.conf 的
	// write list / read list。比 ReadOnly 這個全共享開關更細:
	//   - WriteList 裡的使用者「即使共享是唯讀(ReadOnly=true)也能寫」。
	//   - ReadList 裡的使用者「即使共享是可寫(ReadOnly=false)也只能讀」。
	// 典型用法:ReadOnly=true + WriteList=[alice] = 全部人唯讀、只有 alice 能寫
	// (家庭媒體庫常見);ReadOnly=false + ReadList=[guest] = 全部人可寫、guest
	// 唯讀。兩份名單都是 ValidUsers 的子集語意上才有意義,但 smbd 自己會處理
	// 交集,GoNAS 不強制(留彈性)。空名單不產生對應的設定行。
	WriteList []string `json:"writeList,omitempty"`
	ReadList  []string `json:"readList,omitempty"`

	// Recycle 開啟 Samba 的 vfs_recycle:透過 SMB 刪除的檔案不會立刻消失,
	// 而是移進共享根目錄下一個隱藏的 .recycle/<使用者> 子目錄(保留原本的目錄
	// 結構),等於一個「網路芳鄰的資源回收筒」,手滑刪錯還救得回來。只對
	// SMB 刪除有效(本機/NFS/rsync 的刪除不經過 smbd,不受影響)。
	Recycle bool `json:"recycle,omitempty"`
	// RecycleMaxDays 是回收筒的保留天數:進回收筒超過這麼多天的檔案由背景
	// 清理工作刪除,避免回收筒無限長大把碟塞爆。0(預設)代表永久保留、不自動
	// 清理(由使用者自己管理)。只有 Recycle 為 true 時才有意義。
	RecycleMaxDays int `json:"recycleMaxDays,omitempty"`
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
	if s.RecycleMaxDays < 0 {
		return fmt.Errorf("share %q: recycle retention days cannot be negative", s.Name)
	}
	// 換行/控制字元會破壞格式;逗號是 "... = a, b" 的分隔符,一個含逗號的
	// 「使用者名稱」會被 smbd 當成多個使用者(注入)。第五十六輪覆核(QA6):
	// 空白也一樣——"valid users = alice bob" 會被當成兩個使用者,一個含空白的
	// 名字會被靜默拆成兩個,所以空白也擋掉。write list / read list 跟 valid users
	// 是同一種「逗號分隔的使用者名單」格式,套用同一套檢查。
	for _, list := range []struct {
		label string
		users []string
	}{
		{"valid-user", s.ValidUsers},
		{"write-list", s.WriteList},
		{"read-list", s.ReadList},
	} {
		for _, u := range list.users {
			if hasControlChars(u) || strings.ContainsAny(u, ", \t") {
				return fmt.Errorf("share %q: %s entry %q cannot contain a comma, space, line break, or control character", s.Name, list.label, u)
			}
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
{{- if .WriteList}}
	write list = {{join .WriteList ", "}}
{{- end}}
{{- if .ReadList}}
	read list = {{join .ReadList ", "}}
{{- end}}
{{- if .Recycle}}
	vfs objects = recycle
	recycle:repository = .recycle/%U
	recycle:keeptree = yes
	recycle:versions = yes
	recycle:touch = yes
	recycle:directory_mode = 0770
	recycle:exclude = *.tmp|*.temp|*.log|*.obj|~$*
	recycle:exclude_dir = /tmp|/temp|/cache|/.recycle
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
	// 第五十六輪覆核(QA3):rename 保證的是「換名字」這件事的原子性,不保證暫存
	// 檔的資料真的落到磁碟——斷電時可能 rename 已生效、但檔案內容還是空的/半份。
	// 在 rename 之前先 fsync 暫存檔,把資料真的刷到碟上,對一台會處理斷電事件
	// (UPS)的 NAS 尤其重要。
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("syncing %s: %w", tmpPath, err)
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
	// rename 之後再 fsync 目錄,讓「這個檔名現在指向新 inode」這件事也落碟。
	syncDir(dir)
	return nil
}

// syncDir 盡力 fsync 一個目錄(讓其中的 rename/create 落碟)。失敗不視為致命
// ——不是每個檔案系統都支援對目錄 fsync,而且這是「多一層保險」,不該因此
// 讓一個已經成功的設定寫入回報失敗。
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}

// EnsureSambaInclude 確保系統的 smb.conf 真的用 `include =` 把 GoNAS 產生的
// 共享檔引入——否則我們寫得再漂亮的 gonas-shares.conf,smbd 從來不會讀到,
// 使用者建立的 SMB 共享在網路上永遠看不到(Windows 連進來得到
// BAD_NETWORK_NAME)。
//
// 第五十八輪全鏈路覆核(產品 P2)抓到的核心功能 bug:整個 internal/ 與
// build/ 裡沒有任何一處會加這行 include,而 applySambaConfig 卻回報
// applied:true,UI 顯示「共享已新增」,實際上完全沒生效。
//
// 設計上維持 internal/share 既有的「不覆寫、只增補」原則:
//   - 已經有一行(未被註解的)`include = <includePath>` 就什麼都不做(冪等)。
//   - smb.conf 存在但沒有那行 → 在檔尾補一行。include 是文字層級的引入,
//     被引入的檔案開頭就是各自的 [共享名] 區段,所以放在檔尾也能正確生效,
//     不會被前面某個區段「吃掉」。
//   - smb.conf 不存在(理論上 samba 一裝好就會有;保險起見)→ 建一份最小的
//     含 [global] 的檔案再加 include。
//
// best-effort 的呼叫端會把任何錯誤當成警告回報,不會讓「存共享設定」失敗。
func EnsureSambaInclude(smbConfPath, includePath string) error {
	existing, err := os.ReadFile(smbConfPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("reading %s: %w", smbConfPath, err)
		}
		// 不存在:建一份最小可用的 smb.conf(含 [global])再引入。
		minimal := "# 由 GoNAS 建立的最小 smb.conf —— 只確保 GoNAS 管理的共享檔被引入。\n" +
			"[global]\n" +
			"\tinclude = " + includePath + "\n"
		if err := os.MkdirAll(filepath.Dir(smbConfPath), 0o755); err != nil {
			return fmt.Errorf("creating dir for %s: %w", smbConfPath, err)
		}
		return WriteConfigAtomically(smbConfPath, minimal)
	}

	// 已經有一行未被註解的 include 指到我們的檔案就不重複加。逐行掃描,
	// 略過以 # 或 ; 開頭(去掉前導空白後)的註解行,比對 `include = <path>`
	// (等號兩側空白不拘)。
	for _, line := range strings.Split(string(existing), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";") {
			continue
		}
		low := strings.ToLower(trimmed)
		if strings.HasPrefix(low, "include") {
			rest := strings.TrimSpace(trimmed[len("include"):])
			if strings.HasPrefix(rest, "=") {
				val := strings.TrimSpace(rest[1:])
				if val == includePath {
					return nil // 已經引入過了
				}
			}
		}
	}

	// 沒有 → 在檔尾補一行。保留原本內容原封不動,只在後面接上。
	appended := string(existing)
	if !strings.HasSuffix(appended, "\n") {
		appended += "\n"
	}
	appended += "\n# --- 由 GoNAS 自動加入:引入 GoNAS 管理的共享定義(請勿刪除這行,否則 GoNAS 共享會失效) ---\n" +
		"include = " + includePath + "\n"
	return WriteConfigAtomically(smbConfPath, appended)
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
	// 縱深防禦(第三十輪覆核):跟 SetSystemPassword 一樣,把資料餵進
	// smbpasswd stdin 前先擋控制字元。smbpasswd -s 剛好讀兩行,含 \n 的密碼
	// 比較可能是「把設定弄壞」而非注入,但一致地擋掉才不會留下不同路徑不同
	// 防護的破口。
	if err := ValidatePassword(password); err != nil {
		return err
	}
	stdin := []byte(password + "\n" + password + "\n")
	if _, err := r.RunWithStdin(ctx, stdin, "smbpasswd", "-s", "-a", username); err != nil {
		return fmt.Errorf("syncing samba password for %q: %w", username, err)
	}
	return nil
}
