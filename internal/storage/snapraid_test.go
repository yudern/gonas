package storage

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

func testPoolConfig() PoolConfig {
	return PoolConfig{
		Name:         "tank",
		DataDisks:    []string{"/mnt/disk1", "/mnt/disk2", "/mnt/disk3"},
		ParityDisks:  []string{"/mnt/parity1"},
		MountPoint:   "/mnt/tank",
		ContentFiles: []string{"/mnt/disk1", "/boot/config/snapraid"},
	}
}

func TestGenerateSnapraidConfig(t *testing.T) {
	out, err := GenerateSnapraidConfig(testPoolConfig())
	if err != nil {
		t.Fatalf("GenerateSnapraidConfig returned error: %v", err)
	}

	wantLines := []string{
		"parity /mnt/parity1/snapraid.parity",
		"content /mnt/disk1/snapraid.content",
		"content /boot/config/snapraid/snapraid.content",
		"data d1 /mnt/disk1",
		"data d2 /mnt/disk2",
		"data d3 /mnt/disk3",
	}
	for _, want := range wantLines {
		if !strings.Contains(out, want) {
			t.Errorf("expected generated config to contain %q, got:\n%s", want, out)
		}
	}
}

// 第六十輪回歸(使用者實機根因):不管資料碟以什麼順序傳進來,產出的
// snapraid.conf 的 data 行(dN↔掛載點對應)都必須一模一樣。這正是「重存
// 設定時 lsblk 裝置節點順序變了 → d1/d2 顛倒 → snapraid 報 UUID 互換」的修法。
func TestGenerateSnapraidConfig_DataDiskOrderIsStable(t *testing.T) {
	base := PoolConfig{
		Name:         "tank",
		ParityDisks:  []string{"/mnt/parity1"},
		MountPoint:   "/mnt/tank",
		ContentFiles: []string{"/mnt/disk1", "/mnt/parity1"},
	}
	inOrder := base
	inOrder.DataDisks = []string{"/mnt/disk1", "/mnt/disk2", "/mnt/disk3"}
	reversed := base
	reversed.DataDisks = []string{"/mnt/disk3", "/mnt/disk1", "/mnt/disk2"}

	outA, err := GenerateSnapraidConfig(inOrder)
	if err != nil {
		t.Fatal(err)
	}
	outB, err := GenerateSnapraidConfig(reversed)
	if err != nil {
		t.Fatal(err)
	}
	if outA != outB {
		t.Errorf("expected identical snapraid.conf regardless of data-disk input order.\n--- in-order ---\n%s\n--- reversed ---\n%s", outA, outB)
	}
	// 而且穩定順序就是自然排序:d1=disk1, d2=disk2, d3=disk3。
	for _, want := range []string{"data d1 /mnt/disk1", "data d2 /mnt/disk2", "data d3 /mnt/disk3"} {
		if !strings.Contains(outA, want) {
			t.Errorf("expected stable output to contain %q, got:\n%s", want, outA)
		}
	}
}

func TestNaturalLess(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"/mnt/disk2", "/mnt/disk10", true}, // 2 < 10(自然排序,不是字典序)
		{"/mnt/disk10", "/mnt/disk2", false},
		{"/mnt/disk1", "/mnt/disk2", true},
		{"/mnt/a", "/mnt/b", true},
		{"/mnt/disk2", "/mnt/disk2", false},
	}
	for _, c := range cases {
		if got := naturalLess(c.a, c.b); got != c.want {
			t.Errorf("naturalLess(%q,%q)=%v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestGenerateSnapraidConfig_RejectsInvalidPool(t *testing.T) {
	bad := PoolConfig{Name: "broken"} // 沒有 data/parity disk
	if _, err := GenerateSnapraidConfig(bad); err == nil {
		t.Fatal("expected error generating config for invalid pool, got nil")
	}
}

func TestRunSnapraid_BuildsExpectedCommand(t *testing.T) {
	r := &fakeRunner{output: map[string][]byte{"snapraid": []byte("no differences\n")}}

	out, err := RunSnapraid(context.Background(), r, "/etc/gonas/snapraid-tank.conf", SnapraidDiff)
	if err != nil {
		t.Fatalf("RunSnapraid returned error: %v", err)
	}
	if string(out) != "no differences\n" {
		t.Errorf("unexpected output: %q", out)
	}
	if len(r.calls) != 1 || r.calls[0] != "snapraid" {
		t.Errorf("expected exactly one call to snapraid, got %v", r.calls)
	}
}

func TestRunSnapraid_PropagatesFailure(t *testing.T) {
	r := &fakeRunner{err: map[string]error{"snapraid": errBoom}}
	if _, err := RunSnapraid(context.Background(), r, "/etc/gonas/snapraid-tank.conf", SnapraidSync); err == nil {
		t.Fatal("expected error to propagate from failed snapraid sync")
	}
}

// exitCodeRunner 模擬「執行 snapraid 之後拿到某個結束碼」,但刻意不用假的
// error 型別去騙 errors.As —— 而是真的執行一個會用指定結束碼結束的子行程
// (sh -c "exit N"),再用跟 internal/cmdrunner 完全一樣的包法把它包成
// *exec.ExitError 再包一層 %w。這樣才能確實驗證 RunSnapraid 裡
// errors.As(err, &exitErr) 這段解包邏輯,而不是只驗證一個湊巧符合介面的假錯誤。
//
// 這個 exit code 2 的行為是這次用真正的 snapraid 二進位檔實測到的:
// diff 找到差異時會用 exit code 2 結束,不是 0(詳見
// docs/REAL_HARDWARE_TESTING.md)。
type exitCodeRunner struct {
	exitCode int // 0 表示成功、不設錯誤
	calls    []string
}

func (r *exitCodeRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, name)
	if r.exitCode == 0 {
		return []byte("ok"), nil
	}
	cmd := exec.CommandContext(ctx, "sh", "-c", fmt.Sprintf("exit %d", r.exitCode))
	out, err := cmd.Output()
	if err == nil {
		return out, nil
	}
	// 跟 internal/cmdrunner.runCmd 一樣用 %w 包一層,確保
	// errors.As 需要真的沿著 wrap chain 往下解開才能找到 *exec.ExitError。
	return out, fmt.Errorf("snapraid %v: %w", args, err)
}

func (r *exitCodeRunner) RunWithStdin(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
	return r.Run(ctx, name, args...)
}

func TestRunSnapraid_DiffExitCode2IsNotAnError(t *testing.T) {
	r := &exitCodeRunner{exitCode: 2}

	if _, err := RunSnapraid(context.Background(), r, "/tmp/snapraid.conf", SnapraidDiff); err != nil {
		t.Fatalf("expected diff exit code 2 (differences found) to NOT be an error, got: %v", err)
	}
}

func TestRunSnapraid_DiffExitCode1IsStillAnError(t *testing.T) {
	r := &exitCodeRunner{exitCode: 1}

	if _, err := RunSnapraid(context.Background(), r, "/tmp/snapraid.conf", SnapraidDiff); err == nil {
		t.Fatal("expected diff exit code 1 (real error) to still be reported as an error")
	}
}

func TestRunSnapraid_DiffSuccessNoError(t *testing.T) {
	r := &exitCodeRunner{exitCode: 0}

	if _, err := RunSnapraid(context.Background(), r, "/tmp/snapraid.conf", SnapraidDiff); err != nil {
		t.Fatalf("expected exit code 0 (no differences) to not be an error, got: %v", err)
	}
}

func TestRunSnapraid_SyncExitCode2IsStillAnError(t *testing.T) {
	// sync 沒有 diff 那種「exit code 2 = 有差異」的特殊語意,任何非零結束碼
	// 都必須照舊被當成真正的錯誤,不能被 diff 專用的例外規則誤套用進來。
	r := &exitCodeRunner{exitCode: 2}

	if _, err := RunSnapraid(context.Background(), r, "/tmp/snapraid.conf", SnapraidSync); err == nil {
		t.Fatal("expected sync exit code 2 to still be reported as an error (only diff carves out exit code 2)")
	}
}

func TestRunSnapraid_ScrubExitCode2IsStillAnError(t *testing.T) {
	r := &exitCodeRunner{exitCode: 2}

	if _, err := RunSnapraid(context.Background(), r, "/tmp/snapraid.conf", SnapraidScrub); err == nil {
		t.Fatal("expected scrub exit code 2 to still be reported as an error (only diff carves out exit code 2)")
	}
}

func TestRunSnapraid_DiffNonExitErrorIsStillWrapped(t *testing.T) {
	// 確認一般（非 *exec.ExitError)的錯誤還是照舊被包裝回傳,不會被
	// errors.As 的新分支意外吞掉。
	r := &fakeRunner{err: map[string]error{"snapraid": errBoom}}

	err := func() error {
		_, err := RunSnapraid(context.Background(), r, "/tmp/snapraid.conf", SnapraidDiff)
		return err
	}()
	if err == nil {
		t.Fatal("expected non-exit error to still be reported as an error")
	}
	if !errors.Is(err, errBoom) {
		t.Errorf("expected wrapped error to satisfy errors.Is(err, errBoom), got: %v", err)
	}
}

// argRecordingRunner 記錄每一次呼叫的完整參數,並可依「這次參數含不含
// --force-uuid」回不同結果——用來驗證 RunSnapraidSync 的 UUID 自動重試。
type argRecordingRunner struct {
	calls    [][]string
	plainOut []byte
	plainErr error
	forceOut []byte
	forceErr error
}

func (r *argRecordingRunner) has(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func (r *argRecordingRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, args)
	if r.has(args, "--force-uuid") {
		return r.forceOut, r.forceErr
	}
	return r.plainOut, r.plainErr
}
func (r *argRecordingRunner) RunWithStdin(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
	return r.Run(ctx, name, args...)
}

// 一般情況:sync 一次成功,不該用到 --force-uuid。
func TestRunSnapraidSync_SucceedsWithoutForce(t *testing.T) {
	r := &argRecordingRunner{plainOut: []byte("Everything OK\n")}
	out, forced, err := RunSnapraidSync(context.Background(), r, "/tmp/snapraid.conf")
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if forced {
		t.Error("did not expect --force-uuid on a clean sync")
	}
	if string(out) != "Everything OK\n" {
		t.Errorf("unexpected output %q", out)
	}
	if len(r.calls) != 1 {
		t.Errorf("expected exactly one snapraid call, got %v", r.calls)
	}
}

// 核心回歸:sync 被「太多磁碟 UUID 變了」擋下時,要自動用 --force-uuid 重試
// 一次並成功,forced 回 true。這正是使用者實機「立即同步校驗」失敗的場景。
func TestRunSnapraidSync_RetriesWithForceUUIDOnUUIDChange(t *testing.T) {
	r := &argRecordingRunner{
		plainOut: []byte("UUID change for disk 'd1'\nToo many disks have changed UUIDs since the last sync\n"),
		plainErr: errBoom,
		forceOut: []byte("Everything OK\n"),
	}
	out, forced, err := RunSnapraidSync(context.Background(), r, "/tmp/snapraid.conf")
	if err != nil {
		t.Fatalf("expected the --force-uuid retry to succeed, got %v", err)
	}
	if !forced {
		t.Error("expected forced=true after a UUID-change retry")
	}
	if string(out) != "Everything OK\n" {
		t.Errorf("expected the retry's output, got %q", out)
	}
	if len(r.calls) != 2 {
		t.Fatalf("expected two calls (plain then --force-uuid), got %v", r.calls)
	}
	if r.has(r.calls[0], "--force-uuid") {
		t.Error("first call must NOT include --force-uuid")
	}
	if !r.has(r.calls[1], "--force-uuid") {
		t.Error("second (retry) call must include --force-uuid")
	}
}

// 不是 UUID 問題的一般失敗:不要亂用 --force-uuid,直接把錯誤回傳。
func TestRunSnapraidSync_NonUUIDFailureDoesNotForce(t *testing.T) {
	r := &argRecordingRunner{
		plainOut: []byte("Disk 'd1' is not empty\n"),
		plainErr: errBoom,
	}
	_, forced, err := RunSnapraidSync(context.Background(), r, "/tmp/snapraid.conf")
	if err == nil {
		t.Fatal("expected a non-UUID failure to propagate")
	}
	if forced {
		t.Error("must not use --force-uuid for an unrelated failure")
	}
	if len(r.calls) != 1 {
		t.Errorf("expected exactly one call (no retry), got %v", r.calls)
	}
}

// 連 --force-uuid 也失敗時:回報錯誤,但 forced 仍為 true(讓上層知道試過了)。
func TestRunSnapraidSync_ForceRetryStillFails(t *testing.T) {
	r := &argRecordingRunner{
		plainOut: []byte("Too many disks have changed UUIDs\n"),
		plainErr: errBoom,
		forceOut: []byte("Data error on disk\n"),
		forceErr: errBoom,
	}
	_, forced, err := RunSnapraidSync(context.Background(), r, "/tmp/snapraid.conf")
	if err == nil {
		t.Fatal("expected error when even the --force-uuid retry fails")
	}
	if !forced {
		t.Error("expected forced=true since we did attempt --force-uuid")
	}
	if len(r.calls) != 2 {
		t.Errorf("expected two calls, got %v", r.calls)
	}
}
