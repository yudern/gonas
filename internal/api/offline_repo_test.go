package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bng147/gonas/internal/storage"
)

// TestMain 讓整個 api 套件的測試都不會碰到真實的 /var/lib/gonas*/、
// /etc/apt(沙盒/CI 常以 root 跑測試,真的會寫進去)。
func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "gonas-api-test-")
	if err != nil {
		panic(err)
	}
	offlineRepoDir = filepath.Join(tmp, "gonas-offline-debs")
	legacyOfflineRepoDir = filepath.Join(tmp, "gonas", "debs")
	offlineSourceList = filepath.Join(tmp, "apt", "gonas-offline.list")
	code := m.Run()
	os.RemoveAll(tmp)
	os.Exit(code)
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// withOfflinePaths 把三個路徑變數指到這個測試自己的暫存目錄。
func withOfflinePaths(t *testing.T) (root string) {
	t.Helper()
	root = t.TempDir()
	o1, o2, o3 := offlineRepoDir, legacyOfflineRepoDir, offlineSourceList
	offlineRepoDir = filepath.Join(root, "var/lib/gonas-offline-debs")
	legacyOfflineRepoDir = filepath.Join(root, "var/lib/gonas/debs")
	offlineSourceList = filepath.Join(root, "etc/apt/sources.list.d/gonas-offline.list")
	t.Cleanup(func() { offlineRepoDir, legacyOfflineRepoDir, offlineSourceList = o1, o2, o3 })
	return root
}

// makeLegacyRepo 模擬舊版 late-command:倉庫在 0750 的 /var/lib/gonas/debs,
// 來源清單指向舊位置。
func makeLegacyRepo(t *testing.T) {
	t.Helper()
	if err := os.MkdirAll(legacyOfflineRepoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(legacyOfflineRepoDir), 0o750); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"Packages":                "Package: mergerfs\nFilename: ./mergerfs_2.40_amd64.deb\n",
		"mergerfs_2.40_amd64.deb": "fake",
	} {
		if err := os.WriteFile(filepath.Join(legacyOfflineRepoDir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(offlineSourceList), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(offlineSourceList, []byte("deb [trusted=yes] file://"+legacyOfflineRepoDir+" ./\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureOfflineRepo_MigratesLegacyRepo(t *testing.T) {
	withOfflinePaths(t)
	makeLegacyRepo(t)

	if err := ensureOfflineRepo(discardLogger()); err != nil {
		t.Fatalf("ensureOfflineRepo: %v", err)
	}
	if _, err := os.Stat(filepath.Join(legacyOfflineRepoDir, "Packages")); !os.IsNotExist(err) {
		t.Errorf("legacy repo should have been moved away, stat err=%v", err)
	}
	st, err := os.Stat(offlineRepoDir)
	if err != nil {
		t.Fatalf("new repo dir missing: %v", err)
	}
	if st.Mode().Perm() != 0o755 {
		t.Errorf("new repo dir perm = %o, want 755", st.Mode().Perm())
	}
	for _, f := range []string{"Packages", "mergerfs_2.40_amd64.deb"} {
		fst, err := os.Stat(filepath.Join(offlineRepoDir, f))
		if err != nil {
			t.Fatalf("%s missing after migration: %v", f, err)
		}
		if fst.Mode().Perm() != 0o644 {
			t.Errorf("%s perm = %o, want 644", f, fst.Mode().Perm())
		}
	}
	list, _ := os.ReadFile(offlineSourceList)
	if string(list) != "deb [trusted=yes] file://"+offlineRepoDir+" ./\n" {
		t.Errorf("apt list not rewritten to the new repo: %q", list)
	}

	// 冪等:再跑一次不出錯、內容不變。
	if err := ensureOfflineRepo(discardLogger()); err != nil {
		t.Fatalf("second ensureOfflineRepo: %v", err)
	}
	list2, _ := os.ReadFile(offlineSourceList)
	if string(list2) != string(list) {
		t.Errorf("second run changed the list: %q", list2)
	}
}

func TestEnsureOfflineRepo_SelfHealsMissingList(t *testing.T) {
	withOfflinePaths(t)
	if err := os.MkdirAll(offlineRepoDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(offlineRepoDir, "Packages"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensureOfflineRepo(discardLogger()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(offlineSourceList); err != nil {
		t.Fatalf("list should have been recreated: %v", err)
	}
	st, _ := os.Stat(offlineRepoDir)
	if st.Mode().Perm() != 0o755 {
		t.Errorf("dir perm = %o, want 755", st.Mode().Perm())
	}
}

func TestEnsureOfflineRepo_NoRepoIsNoop(t *testing.T) {
	withOfflinePaths(t)
	if err := ensureOfflineRepo(discardLogger()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(offlineSourceList); !os.IsNotExist(err) {
		t.Errorf("no repo → must not create an apt list (err=%v)", err)
	}
	if _, err := os.Stat(offlineRepoDir); !os.IsNotExist(err) {
		t.Errorf("no repo → must not create the repo dir (err=%v)", err)
	}
}

// 有離線倉庫、離線裝失敗、鏡像不通:回的必須是「離線倉庫安裝失敗」+ apt 真實
// 輸出(detail),絕不能再是「可能沒有聯網」那句誤導訊息。
func TestHandleDoctorInstall_OfflineRepoFailureShowsRealAptError(t *testing.T) {
	withOfflinePaths(t)
	makeLegacyRepo(t)
	oldProbe := mirrorReachable
	mirrorReachable = func(ctx context.Context) bool { return false }
	defer func() { mirrorReachable = oldProbe }()

	s := newTestServer(t)
	s.runner = &recordingRunner{
		failWith: errors.New("exit status 100 (stderr: E: Failed to fetch file:/x/Packages  Permission denied)"),
		failIf:   func(cmd string) bool { return strings.Contains(cmd, "Dir::Etc::sourcelist=") },
	}
	rec := httptest.NewRecorder()
	s.handleDoctorInstall(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"apt":"mergerfs"}`)))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("want 502, got %d: %s", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["error"] != errOfflineInstallFailed.Error() {
		t.Errorf("error = %q, want the offline-repo error", body["error"])
	}
	if strings.Contains(body["error"], "internet") {
		t.Errorf("must not blame the network when an offline repo exists: %q", body["error"])
	}
	if !strings.Contains(body["detail"], "Permission denied") {
		t.Errorf("detail should carry the real apt error, got %q", body["detail"])
	}
	// 而且安裝前已經把倉庫遷到新位置了。
	if _, err := os.Stat(filepath.Join(offlineRepoDir, "Packages")); err != nil {
		t.Errorf("install should have migrated the repo first: %v", err)
	}
}

func TestAptErrorDetail_PicksErrorLines(t *testing.T) {
	out := []byte("Reading package lists...\nErr:1 file:/a ./ Packages\n  Could not open file - open (13: Permission denied)\nE: Unable to locate package mergerfs\n")
	d := aptErrorDetail(out, errors.New("exit status 100"), 12)
	for _, want := range []string{"Err:1", "Permission denied", "E: Unable to locate package mergerfs", "exit status 100"} {
		if !strings.Contains(d, want) {
			t.Errorf("detail missing %q: %q", want, d)
		}
	}
	if strings.Contains(d, "Reading package lists") {
		t.Errorf("detail should drop noise lines: %q", d)
	}
}

// 真實 apt 端到端(只在設了 GONAS_E2E_APT=1 且以 root 跑、有 apt-get/dpkg-deb
// 時才跑):用「0750 父目錄」的舊佈局重現使用者實機的失敗,再驗證遷移後真的
// 能用真實 apt 從離線倉庫裝起來。
func TestOfflineRepo_RealAptEndToEnd(t *testing.T) {
	if os.Getenv("GONAS_E2E_APT") != "1" || os.Geteuid() != 0 {
		t.Skip("set GONAS_E2E_APT=1 and run as root to exercise real apt")
	}
	for _, bin := range []string{"apt-get", "dpkg-deb"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skip(bin + " not available")
		}
	}
	// 必須在 _apt 可遍歷的真實路徑下(t.TempDir 在 /tmp 底下,權限不一定一樣)。
	base := filepath.Join("/var/lib", "gonas-e2e-"+filepath.Base(t.TempDir()))
	t.Cleanup(func() { os.RemoveAll(base); os.RemoveAll(base + "-offline-debs") })
	o1, o2, o3 := offlineRepoDir, legacyOfflineRepoDir, offlineSourceList
	legacyOfflineRepoDir = filepath.Join(base, "debs")
	offlineRepoDir = base + "-offline-debs"
	offlineSourceList = filepath.Join(t.TempDir(), "gonas-offline.list")
	t.Cleanup(func() { offlineRepoDir, legacyOfflineRepoDir, offlineSourceList = o1, o2, o3 })

	// 造一個假 .deb + flat Packages(dpkg-scanpackages 未必有,手寫索引)。
	pkgRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(pkgRoot, "DEBIAN"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctrl := "Package: gonase2etest\nVersion: 1.0\nArchitecture: all\nMaintainer: t <t@t>\nDescription: e2e\n"
	if err := os.WriteFile(filepath.Join(pkgRoot, "DEBIAN/control"), []byte(ctrl), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(legacyOfflineRepoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	deb := filepath.Join(legacyOfflineRepoDir, "gonase2etest_1.0_all.deb")
	if out, err := exec.Command("dpkg-deb", "--build", pkgRoot, deb).CombinedOutput(); err != nil {
		t.Fatalf("dpkg-deb: %v %s", err, out)
	}
	debBytes, err := os.ReadFile(deb)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(debBytes)
	// 跟 fetch-offline-debs.py 產生的索引一樣帶 Size + SHA256(apt 下載時會核對)。
	idx := ctrl + "Filename: ./gonase2etest_1.0_all.deb\nSize: " + itoa(int64(len(debBytes))) +
		"\nSHA256: " + hex.EncodeToString(sum[:]) + "\n\n"
	if err := os.WriteFile(filepath.Join(legacyOfflineRepoDir, "Packages"), []byte(idx), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(base, 0o750); err != nil { // 舊版 install.sh 的權限
		t.Fatal(err)
	}
	if err := os.WriteFile(offlineSourceList, []byte("deb [trusted=yes] file://"+legacyOfflineRepoDir+" ./\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := newTestServer(t)
	s.runner = storage.NewExecRunner()
	oldProbe := mirrorReachable
	mirrorReachable = func(ctx context.Context) bool { return false }
	defer func() { mirrorReachable = oldProbe }()

	// 用 --simulate 不真的動到系統套件資料庫。
	localOnly := func() []string {
		return []string{"-o", "Dir::Etc::sourcelist=" + offlineSourceList, "-o", "Dir::Etc::sourceparts=/dev/null"}
	}
	run := func() ([]byte, error) {
		_, _ = s.runner.Run(context.Background(), "apt-get", append(localOnly(), "update")...)
		return s.runner.Run(context.Background(), "apt-get", append(localOnly(), "install", "-s", "gonase2etest")...)
	}

	// 1) 舊佈局:重現使用者實機的失敗。
	if out, err := run(); err == nil {
		t.Logf("note: legacy layout unexpectedly worked on this apt: %s", out)
	} else {
		t.Logf("legacy layout failed as on the real NAS: %v", err)
	}
	// 2) 直接走 Doctor 真正用的 installOptionalPackage(它會先自動遷移倉庫),
	//    用真實 apt 真的裝起來(沙盒/CI 專用,結束後 purge 掉這個假套件)。
	t.Cleanup(func() { _ = exec.Command("dpkg", "--purge", "gonase2etest").Run() })
	out, err := s.installOptionalPackage(context.Background(), "gonase2etest")
	if err != nil {
		t.Fatalf("Doctor install from the (migrated) offline repo failed: %v\n%s", err, out)
	}
	if o, err := exec.Command("dpkg", "-s", "gonase2etest").CombinedOutput(); err != nil || !strings.Contains(string(o), "install ok installed") {
		t.Fatalf("package not installed after Doctor install: %v\n%s", err, o)
	}
	if _, err := os.Stat(filepath.Join(offlineRepoDir, "Packages")); err != nil {
		t.Fatalf("repo should have been migrated to %s: %v", offlineRepoDir, err)
	}
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
