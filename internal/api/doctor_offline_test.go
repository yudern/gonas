package api

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// recordingRunner 記錄每一次呼叫的完整命令列;可設定「哪種呼叫要回失敗」。
type recordingRunner struct {
	mu       sync.Mutex
	calls    []string
	failIf   func(cmd string) bool // 回 true 的呼叫回失敗
	failWith error
}

func (r *recordingRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := name + " " + strings.Join(args, " ")
	r.mu.Lock()
	r.calls = append(r.calls, cmd)
	r.mu.Unlock()
	if r.failIf != nil && r.failIf(cmd) {
		return []byte("E: simulated failure"), r.failWith
	}
	return nil, nil
}
func (r *recordingRunner) RunWithStdin(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
	return r.Run(ctx, name, args...)
}
func (r *recordingRunner) sawContaining(sub ...string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.calls {
		ok := true
		for _, s := range sub {
			if !strings.Contains(c, s) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// 離線來源存在、且本機安裝成功時:必須「只用本機來源」裝(帶
// Dir::Etc::sourcelist),而且「完全不」跑會連網路的那條 `apt-get update`。
// 第五十九輪的回歸測試:這正是使用者實機(NAS 沒網)卡住的根因。
func TestInstallOptionalPackage_OfflineOnlyWhenBundlePresent(t *testing.T) {
	s := newTestServer(t)
	// 指到一個「存在」的暫存檔,模擬機器上有內建離線來源。
	f := filepath.Join(t.TempDir(), "gonas-offline.list")
	if err := os.WriteFile(f, []byte("deb [trusted=yes] file:///var/lib/gonas/debs ./\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := offlineSourceList
	offlineSourceList = f
	defer func() { offlineSourceList = old }()

	rr := &recordingRunner{} // 全部成功
	s.runner = rr

	if _, err := s.installOptionalPackage(context.Background(), "mergerfs"); err != nil {
		t.Fatalf("expected offline install to succeed, got %v", err)
	}
	if !rr.sawContaining("Dir::Etc::sourcelist="+f, "install", "mergerfs") {
		t.Errorf("expected a local-only apt install (scoped to %s); calls=%v", f, rr.calls)
	}
	// 關鍵:絕不能跑會連網路來源的那條裸 `apt-get update`(沒有 -o 限定來源的)。
	rr.mu.Lock()
	defer rr.mu.Unlock()
	for _, c := range rr.calls {
		if strings.HasSuffix(c, "apt-get update") {
			t.Errorf("offline path must NOT run the network-wide 'apt-get update'; calls=%v", rr.calls)
		}
	}
}

// 離線來源存在、但本機裝不起來(離線包裡沒有這個套件)時:要退回走網路
// (裸 apt-get update + install),對應「機器有網路、要裝離線包沒有的套件」。
func TestInstallOptionalPackage_FallsBackToNetwork(t *testing.T) {
	s := newTestServer(t)
	f := filepath.Join(t.TempDir(), "gonas-offline.list")
	if err := os.WriteFile(f, []byte("deb [trusted=yes] file:///var/lib/gonas/debs ./\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := offlineSourceList
	offlineSourceList = f
	defer func() { offlineSourceList = old }()

	// 第六十輪:離線失敗後只有「鏡像連得到」才走網路退回。測試裡強制探測回 true,
	// 才不會真的去撥外網(沙盒也撥不到),讓「有網路→退回」這條路徑被測到。
	oldProbe := mirrorReachable
	mirrorReachable = func(ctx context.Context) bool { return true }
	defer func() { mirrorReachable = oldProbe }()

	// 讓「本機限定的 install」失敗(命令列含 Dir::Etc::sourcelist 且含 install),
	// 網路那條照常成功。
	rr := &recordingRunner{
		failWith: errors.New("E: Unable to locate package mergerfs"),
		failIf: func(cmd string) bool {
			return strings.Contains(cmd, "Dir::Etc::sourcelist=") && strings.Contains(cmd, "install")
		},
	}
	s.runner = rr

	if _, err := s.installOptionalPackage(context.Background(), "mergerfs"); err != nil {
		t.Fatalf("expected network fallback to succeed, got %v", err)
	}
	if !rr.sawContaining("apt-get update") {
		t.Errorf("expected the network fallback to run a full 'apt-get update'; calls=%v", rr.calls)
	}
}

// 第六十輪:有離線來源、離線裝不起來、而且鏡像連不到(離線 appliance)時,
// 不要空等網路——直接回可行動錯誤,且「絕不」跑會連網的 apt-get update。
func TestInstallOptionalPackage_OfflineAndMirrorUnreachableFailsFast(t *testing.T) {
	s := newTestServer(t)
	f := filepath.Join(t.TempDir(), "gonas-offline.list")
	if err := os.WriteFile(f, []byte("deb [trusted=yes] file:///var/lib/gonas/debs ./\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := offlineSourceList
	offlineSourceList = f
	defer func() { offlineSourceList = old }()

	oldProbe := mirrorReachable
	mirrorReachable = func(ctx context.Context) bool { return false } // 鏡像不通
	defer func() { mirrorReachable = oldProbe }()

	// 本機限定安裝失敗(離線包沒這個套件)。
	rr := &recordingRunner{
		failWith: errors.New("E: Unable to locate package foo"),
		failIf: func(cmd string) bool {
			return strings.Contains(cmd, "Dir::Etc::sourcelist=") && strings.Contains(cmd, "install")
		},
	}
	s.runner = rr
	if _, err := s.installOptionalPackage(context.Background(), "mergerfs"); err == nil {
		t.Fatal("expected an error when offline install fails and the mirror is unreachable")
	}
	for _, c := range rr.calls {
		if strings.HasSuffix(c, "apt-get update") {
			t.Errorf("must NOT run the network 'apt-get update' when the mirror is unreachable; calls=%v", rr.calls)
		}
	}
}

// 沒有離線來源時:直接走網路路徑,不嘗試本機限定安裝。
func TestInstallOptionalPackage_NoOfflineBundle(t *testing.T) {
	s := newTestServer(t)
	old := offlineSourceList
	offlineSourceList = filepath.Join(t.TempDir(), "does-not-exist.list")
	defer func() { offlineSourceList = old }()

	rr := &recordingRunner{}
	s.runner = rr
	if _, err := s.installOptionalPackage(context.Background(), "mergerfs"); err != nil {
		t.Fatalf("expected install to succeed, got %v", err)
	}
	if rr.sawContaining("Dir::Etc::sourcelist=") {
		t.Errorf("with no offline bundle there should be no local-only scoping; calls=%v", rr.calls)
	}
	if !rr.sawContaining("apt-get update") {
		t.Errorf("expected the network path; calls=%v", rr.calls)
	}
}
