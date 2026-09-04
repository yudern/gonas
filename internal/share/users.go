package share

import (
	"context"
	"fmt"
	"regexp"

	"github.com/bng147/gonas/internal/cmdrunner"
)

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
	if _, err := r.Run(ctx, "userdel", username); err != nil {
		return fmt.Errorf("deleting user %q: %w", username, err)
	}
	return nil
}

// SetSystemPassword 透過 chpasswd 設定系統密碼(對應 /etc/shadow)。
// 用 chpasswd 而不是互動式的 passwd,是因為 chpasswd 從 stdin 讀
// "username:password" 這種批次格式，不需要 pty 就能自動化呼叫。
func SetSystemPassword(ctx context.Context, r cmdrunner.Runner, username, password string) error {
	stdin := []byte(username + ":" + password + "\n")
	if _, err := r.RunWithStdin(ctx, stdin, "chpasswd"); err != nil {
		return fmt.Errorf("setting system password for %q: %w", username, err)
	}
	return nil
}

// CreateGroup 建立一個系統群組，用來組織「哪些使用者可以存取哪個共享」。
func CreateGroup(ctx context.Context, r cmdrunner.Runner, name string) error {
	if _, err := r.Run(ctx, "groupadd", name); err != nil {
		return fmt.Errorf("creating group %q: %w", name, err)
	}
	return nil
}
