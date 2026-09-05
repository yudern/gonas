package monitor

import (
	"bufio"
	"context"
	"encoding/base64"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeSMTPServer 是一個最小、但講真正 SMTP 文字協定的假伺服器，跑在
// 127.0.0.1 上一個真實的 TCP 連線之上——這台沙盒的網路白名單連不到
// 任何真的外部郵件服務(Gmail、Office 365 等),沒有辦法像 Phase 9.2
// 那樣對著真正的第三方服務驗證,但用一個會實際做 EHLO/STARTTLS 判斷/
// AUTH PLAIN/MAIL FROM/RCPT TO/DATA 完整交握的假伺服器,至少確保
// EmailNotifier 真的在跟 net/smtp 的用戶端實作做正確的協定交換,而不是
// 對著一個完全繞過網路層、只驗證 Go 函式呼叫參數的 mock——這是這台
// 沙盒能做到的、最接近真實的驗證方式。
//
// 刻意不支援 STARTTLS(EHLO 回應不帶這個擴充):EmailNotifier 對
// STARTTLS 的支援是「伺服器有廣播這個擴充才嘗試」，這裡驗證的是「伺服器
// 沒有廣播的情況下,用戶端會照常用明文完成 AUTH/MAIL/RCPT/DATA」，這正是
// 少見但確實存在的內網中繼情境；伺服器真的支援 STARTTLS 時的行為交給
// net/smtp/net/tls 本身的測試涵蓋，不需要在這裡重新驗證標準函式庫。
type fakeSMTPServer struct {
	listener net.Listener

	mu          chan struct{} // 當作「已經處理完一筆」的訊號量用
	gotFrom     string
	gotTo       []string
	gotAuth     string
	gotDataBody string
}

func newFakeSMTPServer(t *testing.T) *fakeSMTPServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("starting fake SMTP listener: %v", err)
	}
	s := &fakeSMTPServer{listener: ln, mu: make(chan struct{}, 1)}
	go s.serveOne(t)
	t.Cleanup(func() { ln.Close() })
	return s
}

func (s *fakeSMTPServer) addr() string {
	return s.listener.Addr().String()
}

func (s *fakeSMTPServer) serveOne(t *testing.T) {
	conn, err := s.listener.Accept()
	if err != nil {
		return // listener 被 t.Cleanup 關掉時是正常結束，不是測試失敗
	}
	defer conn.Close()

	r := bufio.NewReader(conn)
	writeLine := func(line string) { conn.Write([]byte(line + "\r\n")) }

	writeLine("220 fake.smtp.test ESMTP")

	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")

		switch {
		case strings.HasPrefix(strings.ToUpper(line), "EHLO"):
			writeLine("250-fake.smtp.test greets you")
			writeLine("250 AUTH PLAIN")
		case strings.HasPrefix(strings.ToUpper(line), "AUTH PLAIN"):
			parts := strings.SplitN(line, " ", 3)
			if len(parts) == 3 {
				// AUTH PLAIN <base64> 一行搞定(net/smtp 在伺服器廣播
				// AUTH PLAIN 時就是用這種「initial response」的形式)。
				decoded, _ := base64.StdEncoding.DecodeString(parts[2])
				s.gotAuth = string(decoded)
				writeLine("235 Authentication successful")
			} else {
				// 保守起見也支援「先回 334 再收第二行」這種較舊的形式，
				// 雖然目前這個測試預期用不到。
				writeLine("334 ")
				challenge, _ := r.ReadString('\n')
				decoded, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(challenge))
				s.gotAuth = string(decoded)
				writeLine("235 Authentication successful")
			}
		case strings.HasPrefix(strings.ToUpper(line), "MAIL FROM:"):
			s.gotFrom = extractAngleAddr(line)
			writeLine("250 OK")
		case strings.HasPrefix(strings.ToUpper(line), "RCPT TO:"):
			s.gotTo = append(s.gotTo, extractAngleAddr(line))
			writeLine("250 OK")
		case strings.ToUpper(line) == "DATA":
			writeLine("354 End data with <CR><LF>.<CR><LF>")
			var body strings.Builder
			for {
				dataLine, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if strings.TrimRight(dataLine, "\r\n") == "." {
					break
				}
				body.WriteString(dataLine)
			}
			s.gotDataBody = body.String()
			writeLine("250 OK: queued")
		case strings.ToUpper(line) == "QUIT":
			writeLine("221 Bye")
			select {
			case s.mu <- struct{}{}:
			default:
			}
			return
		default:
			writeLine("500 unrecognized command")
		}
	}
}

func extractAngleAddr(line string) string {
	start := strings.IndexByte(line, '<')
	end := strings.IndexByte(line, '>')
	if start < 0 || end < 0 || end <= start {
		return line
	}
	return line[start+1 : end]
}

func (s *fakeSMTPServer) waitDone(t *testing.T) {
	t.Helper()
	select {
	case <-s.mu:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for the fake SMTP server to see a QUIT")
	}
}

func TestEmailNotifier_Notify_FullSMTPHandshake(t *testing.T) {
	server := newFakeSMTPServer(t)
	host, portStr, err := net.SplitHostPort(server.addr())
	if err != nil {
		t.Fatalf("splitting fake server address: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parsing port: %v", err)
	}

	cfg := EmailConfig{
		ID: "e1", Name: "test-email", Enabled: true,
		SMTPHost: host, SMTPPort: port,
		Username: "alerts@example.com", Password: "hunter2",
		From: "alerts@example.com", To: []string{"oncall@example.com", "backup@example.com"},
	}
	notifier := NewEmailNotifier(cfg, &net.Dialer{Timeout: 2 * time.Second})

	ev := Event{
		Rule:   AlertRule{Name: "High CPU", Metric: MetricCPUPercent, Comparator: ComparatorGT, Threshold: 90},
		Firing: true,
		Value:  97.5,
		At:     time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := notifier.Notify(ctx, ev); err != nil {
		t.Fatalf("Notify returned error: %v", err)
	}
	server.waitDone(t)

	if server.gotFrom != cfg.From {
		t.Errorf("expected MAIL FROM %q, got %q", cfg.From, server.gotFrom)
	}
	if len(server.gotTo) != 2 || server.gotTo[0] != "oncall@example.com" || server.gotTo[1] != "backup@example.com" {
		t.Errorf("expected both recipients to get RCPT TO, got %v", server.gotTo)
	}
	if !strings.Contains(server.gotAuth, "alerts@example.com") || !strings.Contains(server.gotAuth, "hunter2") {
		t.Errorf("expected AUTH PLAIN payload to carry the username/password, got %q", server.gotAuth)
	}
	if !strings.Contains(server.gotDataBody, "High CPU") || !strings.Contains(server.gotDataBody, "FIRING") {
		t.Errorf("expected the email body to mention the rule name and FIRING status, got: %q", server.gotDataBody)
	}
	if !strings.Contains(server.gotDataBody, "Subject: [GoNAS FIRING] High CPU") {
		t.Errorf("expected a subject line reflecting the firing rule, got: %q", server.gotDataBody)
	}
}

func TestEmailNotifier_Notify_ConnectionRefused_ReturnsError(t *testing.T) {
	// 故意連一個沒人在聽的埠——驗證連線失敗會乾淨地回傳錯誤，而不是
	// panic 或無限期卡住(dialer 有逾時)。
	cfg := EmailConfig{
		Name: "unreachable", SMTPHost: "127.0.0.1", SMTPPort: 1,
		From: "a@example.com", To: []string{"b@example.com"},
	}
	notifier := NewEmailNotifier(cfg, &net.Dialer{Timeout: 500 * time.Millisecond})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := notifier.Notify(ctx, Event{Rule: AlertRule{Name: "x", Metric: MetricArrayFailed}, Firing: true, At: time.Now()})
	if err == nil {
		t.Fatal("expected an error connecting to an unreachable SMTP server, got nil")
	}
}

func TestEmailConfig_Validate(t *testing.T) {
	valid := EmailConfig{Name: "n", SMTPHost: "smtp.example.com", SMTPPort: 587, From: "a@example.com", To: []string{"b@example.com"}}
	if err := valid.Validate(); err != nil {
		t.Errorf("expected a well-formed config to validate, got: %v", err)
	}

	cases := []EmailConfig{
		{SMTPHost: "h", SMTPPort: 587, From: "a@example.com", To: []string{"b@example.com"}},           // missing name
		{Name: "n", SMTPPort: 587, From: "a@example.com", To: []string{"b@example.com"}},               // missing host
		{Name: "n", SMTPHost: "h", SMTPPort: 0, From: "a@example.com", To: []string{"b@example.com"}},  // bad port
		{Name: "n", SMTPHost: "h", SMTPPort: 587, From: "not-an-email", To: []string{"b@example.com"}}, // bad from
		{Name: "n", SMTPHost: "h", SMTPPort: 587, From: "a@example.com", To: []string{"not-an-email"}}, // bad to
		{Name: "n", SMTPHost: "h", SMTPPort: 587, From: "a@example.com", To: nil},                      // no recipients
	}
	for i, c := range cases {
		if err := c.Validate(); err == nil {
			t.Errorf("case %d: expected validation to fail for %+v", i, c)
		}
	}
}
