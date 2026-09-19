package doctor

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// withFakeCommandOnPath 在一個臨時目錄裡建立一個可執行的假指令,並把
// PATH 換成只有這個臨時目錄 —— 讓測試能確定地控制「這個指令找不找得
// 到」,不用依賴這台機器實際有沒有裝某個真實工具(這台開發沙盒沒有裝
// mergerfs/snapraid/rsync 等等,但 CI 或使用者機器可能剛好有裝,靠真實
// 環境斷言會不穩定)。
func withFakeCommandOnPath(t *testing.T, name string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("這個測試手法(可執行 shell script + PATH)只在類 Unix 系統上有意義")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("writing fake command: %v", err)
	}
	t.Setenv("PATH", dir)
}

func TestRun_FindsCommandOnPath(t *testing.T) {
	withFakeCommandOnPath(t, "rsync")

	results := Run()

	var found *Result
	for i, r := range results {
		if r.Command == "rsync" {
			found = &results[i]
			break
		}
	}
	if found == nil {
		t.Fatal("expected a check entry for rsync")
	}
	if !found.Found {
		t.Error("expected rsync to be found on the fake PATH")
	}
	if found.Path == "" {
		t.Error("expected a non-empty resolved path for a found command")
	}
}

func TestRun_MissingCommand_ReportsNotFound(t *testing.T) {
	// 一個空的 PATH(只指向一個空目錄),確保清單裡的每個指令都真的
	// 找不到,不受這台跑測試的機器實際裝了什麼影響。
	t.Setenv("PATH", t.TempDir())

	results := Run()

	if len(results) != len(Checks) {
		t.Fatalf("expected %d results, got %d", len(Checks), len(results))
	}
	for _, r := range results {
		if r.Found {
			t.Errorf("expected %q to be reported as not found with an empty PATH, but it was found at %q", r.Command, r.Path)
		}
		if r.Path != "" {
			t.Errorf("expected empty Path for a missing command %q, got %q", r.Command, r.Path)
		}
	}
}

func TestReport_ListsEachCheckAndSummarizesMissing(t *testing.T) {
	withFakeCommandOnPath(t, "rsync")
	results := Run()

	var buf bytes.Buffer
	Report(&buf, results)
	out := buf.String()

	if !strings.Contains(out, "rsync") {
		t.Error("expected report to mention rsync")
	}
	if !strings.Contains(out, "已安裝") {
		t.Error("expected report to mark the found command as installed")
	}
	if !strings.Contains(out, "未安裝") {
		t.Error("expected report to mark missing commands as not installed")
	}
	if !strings.Contains(out, "缺少") {
		t.Error("expected report to summarize how many dependencies are missing")
	}
}

func TestReport_AllFound_PrintsAllInstalledMessage(t *testing.T) {
	dir := t.TempDir()
	for _, c := range Checks {
		path := filepath.Join(dir, c.Command)
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatalf("writing fake command %s: %v", c.Command, err)
		}
	}
	t.Setenv("PATH", dir)

	results := Run()
	for _, r := range results {
		if !r.Found {
			t.Fatalf("expected %q to be found when every command is stubbed on PATH", r.Command)
		}
	}

	var buf bytes.Buffer
	Report(&buf, results)
	if !strings.Contains(buf.String(), "全部依賴都已安裝") {
		t.Errorf("expected the all-installed summary line, got: %s", buf.String())
	}
	if strings.Contains(buf.String(), "缺少") {
		t.Errorf("did not expect a missing-dependencies summary when everything is installed, got: %s", buf.String())
	}
}

func TestRunPackages_ReturnsAllOptionalPackages(t *testing.T) {
	pkgs := RunPackages()
	if len(pkgs) != len(OptionalPackages) {
		t.Fatalf("expected %d packages, got %d", len(OptionalPackages), len(pkgs))
	}
	for _, p := range pkgs {
		if p.Key == "" || p.Apt == "" || len(p.Commands) == 0 {
			t.Errorf("package has empty key/apt/commands: %+v", p)
		}
		// Installed 必須跟 Missing 一致
		if p.Installed && len(p.Missing) != 0 {
			t.Errorf("%s marked installed but has missing commands %v", p.Key, p.Missing)
		}
	}
}

func TestIsInstallable(t *testing.T) {
	for _, ok := range []string{"samba", "mergerfs", "docker.io", "wireguard-tools"} {
		if !IsInstallable(ok) {
			t.Errorf("IsInstallable(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"", "evil", "samba; rm -rf /", "sambaa", "SAMBA", "docker"} {
		if IsInstallable(bad) {
			t.Errorf("IsInstallable(%q) = true, want false", bad)
		}
	}
}
