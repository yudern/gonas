package share

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/bng147/gonas/internal/cmdrunner"
	"github.com/bng147/gonas/internal/textcheck"
)

// ErrPasswordControlChars 是「密碼含控制字元/換行」的固定英文錯誤 —— 前端
// 的 errorMap 會翻成介面語言。這個檢查是第三十輪覆核(資深安全工程師)抓到
// 的漏洞的修補:SetSystemPassword 把 "帳號:密碼\n" 餵給 chpasswd,而 chpasswd
// 一行一組 "帳號:密碼",所以密碼裡若含 \n 就能多注入一行、把「別的帳號
// (例如 root)」的密碼一起改掉。團隊先前把注入防護做在所有「範本型」設定檔
// 產生器上(smb.conf/exports/wg/fstab,見 internal/textcheck),唯獨這條
// stdin 餵 chpasswd 的路徑漏了 —— 這裡補上。
var ErrPasswordControlChars = errors.New("password must not contain control characters or line breaks")

// ValidatePassword 檢查一個要拿去設定系統/Samba 密碼的字串是否安全。目前只擋
// 控制字元(含換行、CR、tab),不對長度/複雜度做要求 —— 那是另一回事,且
// 這個專案的密碼強度規則在 Web 帳號那一側(internal/security),系統帳號這裡
// 只負責「不要讓密碼字串本身破壞 chpasswd 的批次格式或注入額外指令」。
func ValidatePassword(password string) error {
	if textcheck.HasControl(password) {
		return ErrPasswordControlChars
	}
	return nil
}

// usernameRe 刻意比 Linux 實際允許的字元集窄一點:只接受小寫英數字與底線/連字號,
// 開頭不能是數字。這是為了跟 Samba 的使用者名稱慣例(以及避免使用者不小心
// 打出會被當成指令參數的怪字元)保持一致，而不是 useradd 本身能接受的最大範圍。
var usernameRe = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

// User 描述一個 GoNAS 管理的本機使用者帳號。這個帳號同時是 Linux 系統帳號、
// 也是 Samba 使用者 —— NAS 情境下這兩者本來就該是同一個身份，不是兩套獨立系統。
type User struct {
	Username string `json:"username"`
	Comment  string `json:"comment,omitempty"` // 對應 useradd -c，顯示用的全名/描述
}

func (u User) Validate() error {
	if !usernameRe.MatchString(u.Username) {
		return fmt.Errorf("invalid username %q: must match %s", u.Username, usernameRe.String())
	}
	// 第五十八輪資安覆核(#7):Comment 會傳給 useradd -c。雖然它是參數值(無
	// 殼層注入)、useradd 也會擋換行/冒號,但套件裡其他所有欄位都過了控制字元
	// 檢查,唯獨這裡漏了——一致地擋掉才不會留下不同路徑不同防護的破口。
	if textcheck.HasControl(u.Comment) {
		return fmt.Errorf("user comment cannot contain control characters or line breaks")
	}
	return nil
}

// CreateUser 建立一個系統帳號。刻意帶 -M(不建 home 目錄)與
// -s /usr/sbin/nologin(不給互動式登入殼層) —— NAS 使用者是「檔案共享
// 身份」，不是要 SSH 進來的系統管理帳號，不該有殼層存取權。
func CreateUser(ctx context.Context, r cmdrunner.Runner, u User) error {
	if err := u.Validate(); err != nil {
		return fmt.Errorf("refusing to create user: %w", err)
	}
	args := []string{"-M", "-s", "/usr/sbin/nologin"}
	if u.Comment != "" {
		args = append(args, "-c", u.Comment)
	}
	args = append(args, u.Username)

	if _, err := r.Run(ctx, "useradd", args...); err != nil {
		return fmt.Errorf("creating user %q: %w", u.Username, err)
	}
	return nil
}

// DeleteUser 移除一個系統帳號。不預設帶 -r(移除 home 目錄) —— NAS 使用者
// 的「資料」通常是陣列上某個共享目錄的存取權，不是 /home 底下的個人目錄,
// 刪帳號不應該連帶刪掉使用者可能還想保留的東西。
func DeleteUser(ctx context.Context, r cmdrunner.Runner, username string) error {
	// 縱深防禦(第三十輪覆核建議):CreateUser 有驗 username,DeleteUser 之前
	// 沒有 —— 雖然現行呼叫端只會拿 state 裡已驗過的名字來刪,但擋掉「以 -
	// 開頭會被 userdel 當成參數」這類值,才不會因為未來多一個沒驗的呼叫端就
	// 變成參數注入。用同一條 usernameRe(開頭不能是數字/連字號,不含怪字元)。
	if !usernameRe.MatchString(username) {
		return fmt.Errorf("refusing to delete user: invalid username %q", username)
	}
	if _, err := r.Run(ctx, "userdel", username); err != nil {
		return fmt.Errorf("deleting user %q: %w", username, err)
	}
	return nil
}

// SetSystemPassword 透過 chpasswd 設定系統密碼(對應 /etc/shadow)。
// 用 chpasswd 而不是互動式的 passwd,是因為 chpasswd 從 stdin 讀
// "username:password" 這種批次格式，不需要 pty 就能自動化呼叫。
func SetSystemPassword(ctx context.Context, r cmdrunner.Runner, username, password string) error {
	// 縱深防禦:不管呼叫端有沒有先驗過,這個把資料餵進 chpasswd stdin 的
	// 邊界函式一律自己再擋一次控制字元 —— 這是防「chpasswd 換行注入改掉
	// 別的帳號密碼」漏洞的關鍵一道。username 也一併檢查:usernameRe 本來就
	// 不含 ":" 與控制字元,但這個函式的 username 參數是自由字串,擋掉含 ":"
	// 或控制字元的 username 才能保證「帳號:密碼」這一行不會被撐出額外欄位。
	if err := ValidatePassword(password); err != nil {
		return err
	}
	if textcheck.HasControl(username) || strings.Contains(username, ":") {
		return fmt.Errorf("refusing to set system password: invalid username %q", username)
	}
	stdin := []byte(username + ":" + password + "\n")
	if _, err := r.RunWithStdin(ctx, stdin, "chpasswd"); err != nil {
		return fmt.Errorf("setting system password for %q: %w", username, err)
	}
	return nil
}

// CreateGroup 建立一個系統群組，用來組織「哪些使用者可以存取哪個共享」。
func CreateGroup(ctx context.Context, r cmdrunner.Runner, name string) error {
	// 縱深防禦:群組名沿用跟 username 一樣的字元規則(小寫英數字＋底線/連字號、
	// 開頭不能是數字),擋掉會被 groupadd 當成參數或含控制字元的名字。
	if !usernameRe.MatchString(name) {
		return fmt.Errorf("refusing to create group: invalid group name %q", name)
	}
	if _, err := r.Run(ctx, "groupadd", name); err != nil {
		return fmt.Errorf("creating group %q: %w", name, err)
	}
	return nil
}
