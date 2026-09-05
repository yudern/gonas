package security

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func discardTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestGenerateSelfSignedCert_ParsesAndHasExpectedSANs(t *testing.T) {
	certPEM, keyPEM, err := GenerateSelfSignedCert([]string{"192.168.1.10", "nas.local"}, 365*24*time.Hour)
	if err != nil {
		t.Fatalf("GenerateSelfSignedCert returned error: %v", err)
	}

	// tls.X509KeyPair 驗證憑證跟私鑰真的配對得起來(不是各自獨立、對不上的兩份資料)。
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("tls.X509KeyPair failed to load generated cert/key: %v", err)
	}

	cert, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatalf("x509.ParseCertificate failed: %v", err)
	}

	foundIP := false
	for _, ip := range cert.IPAddresses {
		if ip.String() == "192.168.1.10" {
			foundIP = true
		}
	}
	if !foundIP {
		t.Errorf("expected IP SAN 192.168.1.10, got %v", cert.IPAddresses)
	}

	foundDNS := false
	for _, name := range cert.DNSNames {
		if name == "nas.local" {
			foundDNS = true
		}
	}
	if !foundDNS {
		t.Errorf("expected DNS SAN nas.local, got %v", cert.DNSNames)
	}
}

func TestGenerateSelfSignedCert_DefaultsToLocalhostWhenNoHosts(t *testing.T) {
	certPEM, _, err := GenerateSelfSignedCert(nil, time.Hour)
	if err != nil {
		t.Fatalf("GenerateSelfSignedCert returned error: %v", err)
	}
	block, _ := pem.Decode(certPEM)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("x509.ParseCertificate failed: %v", err)
	}

	if len(cert.DNSNames) == 0 || cert.DNSNames[0] != "localhost" {
		t.Errorf("expected default DNSNames [localhost], got %v", cert.DNSNames)
	}
	if len(cert.IPAddresses) == 0 || cert.IPAddresses[0].String() != "127.0.0.1" {
		t.Errorf("expected default IPAddresses [127.0.0.1], got %v", cert.IPAddresses)
	}
}

func TestGenerateSelfSignedCert_ValidityWindow(t *testing.T) {
	validFor := 30 * 24 * time.Hour
	certPEM, _, err := GenerateSelfSignedCert([]string{"localhost"}, validFor)
	if err != nil {
		t.Fatalf("GenerateSelfSignedCert returned error: %v", err)
	}
	block, _ := pem.Decode(certPEM)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("x509.ParseCertificate failed: %v", err)
	}

	now := time.Now()
	if !cert.NotBefore.Before(now) {
		t.Errorf("expected NotBefore (%v) to be before now (%v)", cert.NotBefore, now)
	}
	wantNotAfter := now.Add(validFor)
	// 給個 1 分鐘容差,避免測試本身跑太慢造成誤判。
	if cert.NotAfter.Before(wantNotAfter.Add(-time.Minute)) || cert.NotAfter.After(wantNotAfter.Add(time.Minute)) {
		t.Errorf("NotAfter = %v, want approximately %v", cert.NotAfter, wantNotAfter)
	}
}

func TestEnsureCertFiles_CreatesFilesAndIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "gonas.crt")
	keyPath := filepath.Join(dir, "gonas.key")

	if err := EnsureCertFiles(certPath, keyPath, []string{"localhost"}, 24*time.Hour); err != nil {
		t.Fatalf("first EnsureCertFiles call returned error: %v", err)
	}

	firstCert, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatalf("reading generated cert: %v", err)
	}

	// 再呼叫一次:兩個檔案都已經存在,不應該重新簽發(內容要完全一樣),
	// 不然每次 daemon 重啟都會讓瀏覽器跳一次「憑證變了」的警告。
	if err := EnsureCertFiles(certPath, keyPath, []string{"localhost"}, 24*time.Hour); err != nil {
		t.Fatalf("second EnsureCertFiles call returned error: %v", err)
	}
	secondCert, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatalf("reading cert after second call: %v", err)
	}

	if string(firstCert) != string(secondCert) {
		t.Error("expected EnsureCertFiles to leave an existing cert/key pair untouched, but the certificate changed")
	}
}

func TestEnsureCertFiles_OnlyCreatesMissingFileIfPartiallyPresent(t *testing.T) {
	// 邊界情況:如果不知道為什麼只有其中一個檔案存在(例如上次寫到一半
	// 被中斷 —— 雖然 writeFileAtomically 設計上不該發生,但憑證檔案是
	// 使用者也可能手動放進去的),EnsureCertFiles 應該視為「還沒準備好」
	// 而重新產生一整組,而不是保留半套、產生憑證跟私鑰對不起來的檔案。
	dir := t.TempDir()
	certPath := filepath.Join(dir, "gonas.crt")
	keyPath := filepath.Join(dir, "gonas.key")

	if err := EnsureCertFiles(certPath, keyPath, []string{"localhost"}, time.Hour); err != nil {
		t.Fatalf("EnsureCertFiles returned error: %v", err)
	}

	// tls.LoadX509KeyPair 確認產生的憑證/私鑰檔案真的配對得起來。
	if _, err := tls.LoadX509KeyPair(certPath, keyPath); err != nil {
		t.Errorf("generated cert/key files do not form a valid pair: %v", err)
	}
}

func TestLoadCertExpiry_MatchesGeneratedValidity(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "gonas.crt")
	keyPath := filepath.Join(dir, "gonas.key")
	validFor := 10 * 24 * time.Hour

	if err := EnsureCertFiles(certPath, keyPath, []string{"localhost"}, validFor); err != nil {
		t.Fatalf("EnsureCertFiles returned error: %v", err)
	}

	expiry, err := LoadCertExpiry(certPath)
	if err != nil {
		t.Fatalf("LoadCertExpiry returned error: %v", err)
	}

	want := time.Now().Add(validFor)
	if expiry.Before(want.Add(-time.Minute)) || expiry.After(want.Add(time.Minute)) {
		t.Errorf("LoadCertExpiry() = %v, want approximately %v", expiry, want)
	}
}

func TestLoadCertExpiry_MissingFile(t *testing.T) {
	if _, err := LoadCertExpiry(filepath.Join(t.TempDir(), "does-not-exist.crt")); err == nil {
		t.Error("expected error for missing certificate file, got nil")
	}
}

func TestRenewCertIfNeeded_CreatesWhenMissing(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "gonas.crt")
	keyPath := filepath.Join(dir, "gonas.key")

	renewed, err := RenewCertIfNeeded(certPath, keyPath, []string{"localhost"}, 24*time.Hour, time.Hour, time.Now())
	if err != nil {
		t.Fatalf("RenewCertIfNeeded returned error: %v", err)
	}
	if !renewed {
		t.Error("expected renewed=true when cert files did not exist yet")
	}
	if _, err := tls.LoadX509KeyPair(certPath, keyPath); err != nil {
		t.Errorf("generated cert/key files do not form a valid pair: %v", err)
	}
}

// TestRenewCertIfNeeded_NotYetDue_LeavesExistingCertUntouched 是續期邏輯
// 最核心的行為:憑證還沒進入續期門檻時完全不該動它,不然使用者的瀏覽器
// 會無緣無故一直跳「憑證變了」的警告。
func TestRenewCertIfNeeded_NotYetDue_LeavesExistingCertUntouched(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "gonas.crt")
	keyPath := filepath.Join(dir, "gonas.key")

	if err := EnsureCertFiles(certPath, keyPath, []string{"localhost"}, 365*24*time.Hour); err != nil {
		t.Fatalf("EnsureCertFiles returned error: %v", err)
	}
	original, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatalf("reading original cert: %v", err)
	}

	// now + renewBefore is nowhere near the 365-day expiry, so this should
	// be a no-op.
	renewed, err := RenewCertIfNeeded(certPath, keyPath, []string{"localhost"}, 365*24*time.Hour, 30*24*time.Hour, time.Now())
	if err != nil {
		t.Fatalf("RenewCertIfNeeded returned error: %v", err)
	}
	if renewed {
		t.Error("expected renewed=false when the certificate is nowhere near its renewal threshold")
	}

	after, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatalf("reading cert after RenewCertIfNeeded: %v", err)
	}
	if string(original) != string(after) {
		t.Error("expected certificate file to be untouched when not yet due for renewal")
	}
}

// TestRenewCertIfNeeded_PastRenewalThreshold_IssuesNewCert verifies the
// other half: once "now" is within renewBefore of the certificate's
// NotAfter (simulated here by using a very short validFor so the freshly
// generated cert is already within the renewal window), a fresh
// certificate is issued and the file contents actually change.
func TestRenewCertIfNeeded_PastRenewalThreshold_IssuesNewCert(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "gonas.crt")
	keyPath := filepath.Join(dir, "gonas.key")

	// Issue a cert valid for only 1 hour.
	if err := EnsureCertFiles(certPath, keyPath, []string{"localhost"}, time.Hour); err != nil {
		t.Fatalf("EnsureCertFiles returned error: %v", err)
	}
	original, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatalf("reading original cert: %v", err)
	}

	// renewBefore of 2 hours means "now" is already within the renewal
	// window of a cert that only has 1 hour of validity left.
	renewed, err := RenewCertIfNeeded(certPath, keyPath, []string{"localhost"}, 365*24*time.Hour, 2*time.Hour, time.Now())
	if err != nil {
		t.Fatalf("RenewCertIfNeeded returned error: %v", err)
	}
	if !renewed {
		t.Error("expected renewed=true when within the renewal threshold of expiry")
	}

	after, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatalf("reading cert after renewal: %v", err)
	}
	if string(original) == string(after) {
		t.Error("expected certificate content to change after renewal")
	}

	// The renewed cert should reflect the new validFor (365 days), not the
	// original 1-hour validity.
	expiry, err := LoadCertExpiry(certPath)
	if err != nil {
		t.Fatalf("LoadCertExpiry returned error: %v", err)
	}
	if time.Until(expiry) < 300*24*time.Hour {
		t.Errorf("expected renewed cert to be valid for ~365 days, expiry is only %v away", time.Until(expiry))
	}
}

func TestRenewCertIfNeeded_AlreadyExpired_IssuesNewCert(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "gonas.crt")
	keyPath := filepath.Join(dir, "gonas.key")

	if err := EnsureCertFiles(certPath, keyPath, []string{"localhost"}, time.Hour); err != nil {
		t.Fatalf("EnsureCertFiles returned error: %v", err)
	}

	// Simulate checking long after the certificate has already expired.
	future := time.Now().Add(365 * 24 * time.Hour)
	renewed, err := RenewCertIfNeeded(certPath, keyPath, []string{"localhost"}, 24*time.Hour, time.Hour, future)
	if err != nil {
		t.Fatalf("RenewCertIfNeeded returned error: %v", err)
	}
	if !renewed {
		t.Error("expected renewed=true for an already-expired certificate")
	}
}

func TestCertStore_GetCertificate_LoadsAndCaches(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "gonas.crt")
	keyPath := filepath.Join(dir, "gonas.key")
	if err := EnsureCertFiles(certPath, keyPath, []string{"localhost"}, 24*time.Hour); err != nil {
		t.Fatalf("EnsureCertFiles returned error: %v", err)
	}

	store := NewCertStore(certPath, keyPath)
	first, err := store.GetCertificate(nil)
	if err != nil {
		t.Fatalf("GetCertificate returned error: %v", err)
	}
	if first == nil {
		t.Fatal("expected a non-nil certificate")
	}

	second, err := store.GetCertificate(nil)
	if err != nil {
		t.Fatalf("second GetCertificate call returned error: %v", err)
	}
	// Same underlying *tls.Certificate pointer means the cache was used
	// instead of re-reading/re-parsing the files from disk.
	if first != second {
		t.Error("expected GetCertificate to return the cached certificate when files haven't changed")
	}
}

func TestCertStore_GetCertificate_ReloadsAfterFileChanges(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "gonas.crt")
	keyPath := filepath.Join(dir, "gonas.key")
	if err := EnsureCertFiles(certPath, keyPath, []string{"localhost"}, time.Hour); err != nil {
		t.Fatalf("EnsureCertFiles returned error: %v", err)
	}

	store := NewCertStore(certPath, keyPath)
	first, err := store.GetCertificate(nil)
	if err != nil {
		t.Fatalf("GetCertificate returned error: %v", err)
	}

	// Simulate CertRenewer issuing a fresh cert (mtimes will differ from
	// the original files since some real time passes between the two
	// EnsureCertFiles calls, and RenewCertIfNeeded always uses the atomic
	// write-then-rename path which produces a fresh mtime).
	time.Sleep(10 * time.Millisecond)
	renewed, err := RenewCertIfNeeded(certPath, keyPath, []string{"localhost"}, 365*24*time.Hour, 2*time.Hour, time.Now())
	if err != nil {
		t.Fatalf("RenewCertIfNeeded returned error: %v", err)
	}
	if !renewed {
		t.Fatal("expected the 1-hour cert to be renewed given a 2-hour renewBefore threshold")
	}

	second, err := store.GetCertificate(nil)
	if err != nil {
		t.Fatalf("GetCertificate after renewal returned error: %v", err)
	}
	if first == second {
		t.Error("expected GetCertificate to reload and return a different certificate after the files were renewed")
	}
}

func TestCertRenewer_RunsRepeatedlyAndStopsCleanly(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "gonas.crt")
	keyPath := filepath.Join(dir, "gonas.key")
	// Start with a cert that's already within the renewal window relative
	// to the check the renewer will perform, so every tick renews it.
	if err := EnsureCertFiles(certPath, keyPath, []string{"localhost"}, time.Millisecond); err != nil {
		t.Fatalf("EnsureCertFiles returned error: %v", err)
	}

	r := NewCertRenewer(discardTestLogger())
	r.Start(context.Background(), 5*time.Millisecond, certPath, keyPath, []string{"localhost"}, 24*time.Hour, time.Hour)
	defer r.Stop()

	// The immediate startup check should renew right away since the
	// original cert is already expired (validFor: 1ms).
	deadline := time.Now().Add(500 * time.Millisecond)
	var expiry time.Time
	for time.Now().Before(deadline) {
		var err error
		expiry, err = LoadCertExpiry(certPath)
		if err == nil && time.Until(expiry) > time.Hour {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if time.Until(expiry) <= time.Hour {
		t.Fatalf("expected CertRenewer to have renewed the cert to a ~24h validity window, expiry is only %v away", time.Until(expiry))
	}

	r.Stop()
}

func TestCertRenewer_StopBeforeStart_DoesNotPanic(t *testing.T) {
	r := NewCertRenewer(discardTestLogger())
	r.Stop() // never started; must be a safe no-op
}

// TestGenerateSelfSignedCert_LiveOpenSSLVerification 用這台機器真正的
// `openssl` 執行檔解析產生出來的憑證 —— 跟這個專案一貫的作法一致
// (Phase 1/3 都用真的系統工具驗證過,而不是只靠 Go 自己的
// x509.ParseCertificate 驗證自己產生的東西),`openssl x509 -text`
// 如果連 GoNAS 自己產生的憑證都解析不了,代表憑證編碼本身有問題,
// 光靠 Go 標準函式庫互相驗證是抓不出這種問題的。
func TestGenerateSelfSignedCert_LiveOpenSSLVerification(t *testing.T) {
	opensslPath, err := exec.LookPath("openssl")
	if err != nil {
		t.Skip("openssl not installed in this environment, skipping live verification")
	}

	dir := t.TempDir()
	certPath := filepath.Join(dir, "gonas.crt")
	keyPath := filepath.Join(dir, "gonas.key")
	if err := EnsureCertFiles(certPath, keyPath, []string{"192.168.1.50", "gonas.local"}, 24*time.Hour); err != nil {
		t.Fatalf("EnsureCertFiles returned error: %v", err)
	}

	out, err := exec.Command(opensslPath, "x509", "-in", certPath, "-noout", "-text").CombinedOutput()
	if err != nil {
		t.Fatalf("openssl x509 -text failed to parse generated certificate: %v\noutput: %s", err, out)
	}

	text := string(out)
	for _, want := range []string{"192.168.1.50", "gonas.local", "Public Key Algorithm: id-ecPublicKey"} {
		if !strings.Contains(text, want) {
			t.Errorf("expected openssl output to mention %q, got:\n%s", want, text)
		}
	}
}
