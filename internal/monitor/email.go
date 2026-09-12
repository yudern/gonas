package monitor

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"
)

// EmailConfig 是使用者設定的一個 email 通知管道，持久化在 internal/state
// 裡(見 state.State.EmailNotifiers)。設計上刻意跟 WebhookConfig 平行——
// 同樣是「使用者填一組設定、AlertEngine 觸發時送出去」的模式，只是
// 送達方式從 HTTP POST 換成 SMTP。
//
// 跟 WebhookConfig 不一樣的地方:Webhook 只需要一個 URL，Email 需要一組
// SMTP 憑證(Username/Password)才能透過大多數郵件服務(Gmail、Office 365
// 等)的中繼伺服器送信——這組密碼沒有辦法像登入密碼那樣只存雜湊值,
// 因為每次送信都要拿它去跟 SMTP 伺服器做明文/加密通道下的身分驗證,
// 伺服器端沒辦法反推雜湊值。這是 SMTP 這個協定本身的限制,不是 GoNAS
// 的設計疏漏——跟大多數會整合第三方通知服務的軟體(例如需要存
// API Token 而不是只存雜湊)是同樣的取捨,前端會清楚提示這一點。
type EmailConfig struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Enabled  bool     `json:"enabled"`
	SMTPHost string   `json:"smtpHost"`
	SMTPPort int      `json:"smtpPort"`
	Username string   `json:"username,omitempty"` // 留空代表這個中繼伺服器不需要身分驗證(少見,但內網中繼常見)
	Password string   `json:"password,omitempty"`
	From     string   `json:"from"`
	To       []string `json:"to"`
}

// looksLikeEmailAddress 只做最基本的「有沒有 @、@ 前後都不是空字串」
// 檢查，不是完整的 RFC 5322 驗證——真正能不能送達，最終還是要看 SMTP
// 伺服器本身怎麼回應，這裡的驗證純粹是擋掉「明顯打錯、忘記填 @」這種
// 低級輸入錯誤，不需要為了「看起來很完整」而引入一個複雜的驗證規則,
// 之後反而可能誤判合法但少見的地址格式。
func looksLikeEmailAddress(addr string) bool {
	at := strings.IndexByte(addr, '@')
	return at > 0 && at < len(addr)-1
}

// Validate 檢查 email 設定本身，不會真的連線到 SMTP 伺服器——跟
// WebhookConfig.Validate 一樣的考量:靜態欄位檢查在儲存當下就能做,
// 「連不連得上、帳密對不對」這種需要真的連線才能確認的事,留到
// 送出通知的當下由 Notify 的錯誤回饋處理。
func (c EmailConfig) Validate() error {
	if c.Name == "" {
		return fmt.Errorf("notifier name is required")
	}
	if c.SMTPHost == "" {
		return fmt.Errorf("smtp host is required")
	}
	if c.SMTPPort <= 0 || c.SMTPPort > 65535 {
		return fmt.Errorf("smtp port must be between 1 and 65535, got %d", c.SMTPPort)
	}
	if !looksLikeEmailAddress(c.From) {
		return fmt.Errorf("from address %q does not look like a valid email address", c.From)
	}
	if len(c.To) == 0 {
		return fmt.Errorf("at least one recipient (\"to\") is required")
	}
	for _, addr := range c.To {
		if !looksLikeEmailAddress(addr) {
			return fmt.Errorf("recipient address %q does not look like a valid email address", addr)
		}
	}
	return nil
}

// EmailNotifier 用標準函式庫的 net/smtp 把 Event 組成一封信送出去——
// 跟這個專案其他地方(internal/docker、WebhookNotifier)一樣，刻意不用
// 任何第三方郵件套件。
//
// net/smtp 本身沒有 context 支援(這是它比較舊、後來被官方標記為「不再
// 主動維護」的原因之一，但仍然是標準函式庫的一部分，沒有替代品)，這裡
// 沒有直接呼叫 smtp.SendMail,而是自己用 net.Dialer.DialContext 建立
// 連線再交給 smtp.NewClient 接手——這樣至少「連線」這一步能真的被
// ctx 取消/逾時,跟 WebhookNotifier 用 http.NewRequestWithContext 是
// 同樣的考量,只是 SMTP 協定的握手步驟比一次 HTTP 請求多，沒辦法讓
// 整個交握過程都掛在同一個 context 上，只能盡量在每一步都檢查。
type EmailNotifier struct {
	config EmailConfig
	dialer *net.Dialer
}

// NewEmailNotifier 建立一個 EmailNotifier。dialer 為 nil 時使用一個有
// 10 秒連線逾時的預設值——SMTP 交握(EHLO/STARTTLS/AUTH/MAIL/RCPT/DATA)
// 比一次單純的 HTTP 請求多好幾個來回，10 秒比 WebhookNotifier 的 5 秒
// 更寬裕，避免對著較慢的中繼伺服器時逾時太早；測試可以傳入指向
// 127.0.0.1 假 SMTP 伺服器、逾時更短的 dialer。
func NewEmailNotifier(cfg EmailConfig, dialer *net.Dialer) *EmailNotifier {
	if dialer == nil {
		dialer = &net.Dialer{Timeout: 10 * time.Second}
	}
	return &EmailNotifier{config: cfg, dialer: dialer}
}

func (n *EmailNotifier) Notify(ctx context.Context, ev Event) error {
	addr := net.JoinHostPort(n.config.SMTPHost, fmt.Sprintf("%d", n.config.SMTPPort))

	conn, err := n.dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("connecting to SMTP server %q: %w", addr, err)
	}

	client, err := smtp.NewClient(conn, n.config.SMTPHost)
	if err != nil {
		conn.Close()
		return fmt.Errorf("initializing SMTP client for %q: %w", n.config.Name, err)
	}
	defer client.Close()

	// 能力允許就升級成 STARTTLS 再繼續——大多數郵件服務(Gmail、
	// Office 365 等)的中繼伺服器要求 STARTTLS 之後才准許 AUTH,少見的
	// 內網中繼可能完全不支援,這種情況才退回明文,而不是強制要求。
	if ok, _ := client.Extension("STARTTLS"); ok {
		if err := client.StartTLS(&tls.Config{ServerName: n.config.SMTPHost}); err != nil {
			return fmt.Errorf("starting TLS with SMTP server %q: %w", n.config.Name, err)
		}
	}

	if n.config.Username != "" {
		if ok, _ := client.Extension("AUTH"); ok {
			auth := smtp.PlainAuth("", n.config.Username, n.config.Password, n.config.SMTPHost)
			if err := client.Auth(auth); err != nil {
				return fmt.Errorf("authenticating with SMTP server %q: %w", n.config.Name, err)
			}
		}
	}

	if err := client.Mail(n.config.From); err != nil {
		return fmt.Errorf("MAIL FROM failed for %q: %w", n.config.Name, err)
	}
	for _, to := range n.config.To {
		if err := client.Rcpt(to); err != nil {
			return fmt.Errorf("RCPT TO %q failed for %q: %w", to, n.config.Name, err)
		}
	}

	wc, err := client.Data()
	if err != nil {
		return fmt.Errorf("SMTP DATA failed for %q: %w", n.config.Name, err)
	}
	if _, err := wc.Write([]byte(buildEmailMessage(n.config, ev))); err != nil {
		wc.Close()
		return fmt.Errorf("writing email body for %q: %w", n.config.Name, err)
	}
	if err := wc.Close(); err != nil {
		return fmt.Errorf("finishing SMTP DATA for %q: %w", n.config.Name, err)
	}

	return client.Quit()
}

// buildEmailMessage 組出一封最小、合法的 RFC 5322 郵件(標頭 + 空行 +
// 內文)。刻意只用最基本的標頭(From/To/Subject/Date)跟純文字內文——
// 告警通知的內容本來就單純,沒有必要為了排版做 HTML 郵件,純文字在任何
// 郵件用戶端都能正常顯示,也不會有 HTML 郵件常見的跑版/垃圾信過濾問題。
func buildEmailMessage(cfg EmailConfig, ev Event) string {
	var subject string
	var body strings.Builder

	if ev.Kind == EventKindDigest || ev.Kind == EventKindBackupFailed {
		// Digest 與備份失敗通知的標題/內文都已經是組好給人看的文字,
		// 這裡不用像告警事件那樣從 Rule/Value 組欄位,直接使用
		// Subject/Message 即可。
		subject = fmt.Sprintf("[GoNAS] %s", ev.Subject)
		body.WriteString(ev.Message)
	} else {
		status := "RESOLVED"
		if ev.Firing {
			status = "FIRING"
		}
		subject = fmt.Sprintf("[GoNAS %s] %s", status, ev.Rule.Name)

		fmt.Fprintf(&body, "GoNAS alert: %s\n\n", status)
		fmt.Fprintf(&body, "Rule:      %s\n", ev.Rule.Name)
		fmt.Fprintf(&body, "Metric:    %s\n", ev.Rule.Metric)
		if !ev.Rule.Metric.isBoolean() {
			fmt.Fprintf(&body, "Condition: %s %s %v\n", ev.Rule.Metric, ev.Rule.Comparator, ev.Rule.Threshold)
		}
		fmt.Fprintf(&body, "Value:     %v\n", ev.Value)
		fmt.Fprintf(&body, "Time:      %s\n", ev.At.Format(time.RFC1123Z))
	}

	var msg strings.Builder
	fmt.Fprintf(&msg, "From: %s\r\n", cfg.From)
	fmt.Fprintf(&msg, "To: %s\r\n", strings.Join(cfg.To, ", "))
	fmt.Fprintf(&msg, "Subject: %s\r\n", subject)
	fmt.Fprintf(&msg, "Date: %s\r\n", ev.At.Format(time.RFC1123Z))
	msg.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	msg.WriteString("\r\n")
	// SMTP DATA 的內文一律要求 CRLF 換行，body 是用 \n 組的，這裡統一轉換。
	msg.WriteString(strings.ReplaceAll(body.String(), "\n", "\r\n"))
	return msg.String()
}
