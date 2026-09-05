package security

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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
