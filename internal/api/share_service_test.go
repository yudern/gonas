package api

import (
	"context"
	"errors"
	"testing"
)

// 第六十輪:建立共享時,GoNAS 要把對應服務 enable --now(appliance 預設不自動
// 啟用,靠這個在使用者真的要用時才拉起來)。
func TestEnsureServicesRunning_EnablesAndStartsEach(t *testing.T) {
	s := newTestServer(t)
	rr := &recordingRunner{}
	s.runner = rr
	s.ensureServicesRunning(context.Background(), "smbd", "nmbd")
	if !rr.sawContaining("systemctl", "enable", "--now", "smbd") {
		t.Errorf("expected `systemctl enable --now smbd`; calls=%v", rr.calls)
	}
	if !rr.sawContaining("systemctl", "enable", "--now", "nmbd") {
		t.Errorf("expected `systemctl enable --now nmbd`; calls=%v", rr.calls)
	}
}

// 某個服務沒裝(enable 失敗)不該中斷,其餘服務照常嘗試(best-effort)。
func TestEnsureServicesRunning_BestEffortContinuesOnFailure(t *testing.T) {
	s := newTestServer(t)
	rr := &recordingRunner{
		failWith: errors.New("unit not found"),
		failIf:   func(cmd string) bool { return true }, // 全部失敗
	}
	s.runner = rr
	// 不應 panic、不應回傳(本來就沒有回傳值);兩個服務都有嘗試。
	s.ensureServicesRunning(context.Background(), "smbd", "nmbd")
	if len(rr.calls) != 2 {
		t.Errorf("expected both services to be attempted even when the first fails; calls=%v", rr.calls)
	}
}
