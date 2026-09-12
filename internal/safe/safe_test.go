package safe

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// TestRun_RecoversPanic 確認 fn panic 時 Run 會攔下來(不往外 re-panic)、
// 而且把 panic 內容記進 log。
func TestRun_RecoversPanic(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	// 如果 Run 沒有 recover,這個 panic 會讓測試 goroutine 崩潰、整個
	// 測試進程掛掉;能正常往下走到斷言,本身就證明 panic 被攔住了。
	Run(logger, "unit-test-task", func() {
		panic("boom")
	})

	out := buf.String()
	if !strings.Contains(out, "unit-test-task") {
		t.Errorf("expected log to name the task label, got: %s", out)
	}
	if !strings.Contains(out, "boom") {
		t.Errorf("expected log to include the panic value, got: %s", out)
	}
	if !strings.Contains(out, "panicked") {
		t.Errorf("expected a panic-recovered log line, got: %s", out)
	}
}

// TestRun_NoPanicRunsNormally 確認沒有 panic 時 fn 正常執行、不記任何
// error log。
func TestRun_NoPanicRunsNormally(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	ran := false
	Run(logger, "ok-task", func() { ran = true })

	if !ran {
		t.Fatal("expected fn to run")
	}
	if buf.Len() != 0 {
		t.Errorf("expected no log output on the happy path, got: %s", buf.String())
	}
}

// TestRun_NilLoggerIsSafe 確認 logger 為 nil 時 recover 仍然安全(不會
// 因為想記 log 反而自己 panic)。
func TestRun_NilLoggerIsSafe(t *testing.T) {
	Run(nil, "nil-logger-task", func() { panic("still recovered") })
	// 能執行到這裡就代表沒有二次 panic。
}
