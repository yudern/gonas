package timekeep

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeRunner 回放預先準備好的 timedatectl 輸出/錯誤。
type fakeRunner struct {
	out    map[string][]byte // key: 第一個 args(show / list-timezones / set-timezone ...)
	err    map[string]error
	called [][]string
}

func (f *fakeRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	f.called = append(f.called, append([]string{name}, args...))
	key := ""
	if len(args) > 0 {
		key = args[0]
	}
	return f.out[key], f.err[key]
}
func (f *fakeRunner) RunWithStdin(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
	return f.Run(ctx, name, args...)
}

func TestGetStatusParsesTimedatectl(t *testing.T) {
	r := &fakeRunner{out: map[string][]byte{
		"show": []byte("Timezone=Asia/Shanghai\nNTP=yes\nNTPSynchronized=yes\n"),
	}}
	st := GetStatus(context.Background(), r)
	if !st.CanManage {
		t.Fatal("timedatectl 可用時 CanManage 應為 true")
	}
	if st.Timezone != "Asia/Shanghai" {
		t.Errorf("Timezone=%q", st.Timezone)
	}
	if !st.NTPEnabled || !st.NTPSynchronized {
		t.Errorf("NTP 狀態解析錯誤:%+v", st)
	}
	if st.UnixSeconds == 0 {
		t.Error("UnixSeconds 應填入")
	}
}

func TestGetStatusFallbackWhenNoTimedatectl(t *testing.T) {
	dir := t.TempDir()
	tzFile := filepath.Join(dir, "timezone")
	os.WriteFile(tzFile, []byte("Europe/Paris\n"), 0o644)
	old := EtcTimezone
	EtcTimezone = tzFile
	defer func() { EtcTimezone = old }()

	r := &fakeRunner{err: map[string]error{"show": os.ErrNotExist}}
	st := GetStatus(context.Background(), r)
	if st.CanManage {
		t.Fatal("timedatectl 不可用時 CanManage 應為 false")
	}
	if st.Timezone != "Europe/Paris" {
		t.Errorf("退回讀 /etc/timezone 失敗:%q", st.Timezone)
	}
}

func TestValidTimezone(t *testing.T) {
	for _, tz := range []string{"Asia/Shanghai", "UTC", "America/Argentina/Buenos_Aires", "Europe/Paris"} {
		if !ValidTimezone(tz) {
			t.Errorf("%q 應為合法時區", tz)
		}
	}
	for _, tz := range []string{"", "../../etc/passwd", "Asia/Shanghai; rm -rf /", "Not/A/Real/Zone/Deep", "foo$(bar)"} {
		if ValidTimezone(tz) {
			t.Errorf("%q 不應通過驗證", tz)
		}
	}
}

func TestSetTimezoneRejectsInvalid(t *testing.T) {
	r := &fakeRunner{}
	if err := SetTimezone(context.Background(), r, "../evil"); err == nil {
		t.Fatal("非法時區應被拒絕,且不呼叫 timedatectl")
	}
	if len(r.called) != 0 {
		t.Errorf("非法時區不該呼叫 timedatectl,卻呼叫了:%v", r.called)
	}
}

func TestSetTimezoneCallsTimedatectl(t *testing.T) {
	r := &fakeRunner{}
	if err := SetTimezone(context.Background(), r, "Asia/Taipei"); err != nil {
		t.Fatal(err)
	}
	if len(r.called) != 1 || r.called[0][1] != "set-timezone" || r.called[0][2] != "Asia/Taipei" {
		t.Errorf("timedatectl 呼叫不正確:%v", r.called)
	}
}

func TestSetNTP(t *testing.T) {
	r := &fakeRunner{}
	SetNTP(context.Background(), r, true)
	SetNTP(context.Background(), r, false)
	if r.called[0][2] != "true" || r.called[1][2] != "false" {
		t.Errorf("set-ntp 參數錯誤:%v", r.called)
	}
}

func TestListTimezonesWalkFallback(t *testing.T) {
	dir := t.TempDir()
	// 造一個假的 zoneinfo:Asia/Shanghai、Europe/Paris
	for _, z := range []string{"Asia/Shanghai", "Europe/Paris", "America/New_York"} {
		p := filepath.Join(dir, z)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("TZif"), 0o644)
	}
	old := ZoneinfoDir
	ZoneinfoDir = dir
	defer func() { ZoneinfoDir = old }()

	r := &fakeRunner{err: map[string]error{"list-timezones": os.ErrNotExist}}
	zs := ListTimezones(context.Background(), r)
	want := map[string]bool{"Asia/Shanghai": true, "Europe/Paris": true, "America/New_York": true}
	got := map[string]bool{}
	for _, z := range zs {
		got[z] = true
	}
	for z := range want {
		if !got[z] {
			t.Errorf("列舉時區缺少 %q(得到 %v)", z, zs)
		}
	}
}

func TestSetTimeFormats(t *testing.T) {
	r := &fakeRunner{}
	tm := time.Date(2026, 10, 8, 23, 45, 0, 0, time.UTC)
	SetTime(context.Background(), r, tm)
	if r.called[0][2] != "2026-10-08 23:45:00" {
		t.Errorf("set-time 格式錯誤:%v", r.called)
	}
}
