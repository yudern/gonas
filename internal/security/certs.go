package security

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// GenerateSelfSignedCert 產生一份自簽 TLS 憑證。hosts 可以混雜 IP 位址
// (例如 "192.168.1.10")跟 DNS 名稱(例如 "nas.local"),個別歸類進
// 憑證的 SAN(Subject Alternative Name)欄位 —— 現代瀏覽器只認 SAN,
// 不看 Common Name,這裡兩者都填只是為了跟少數還在看 CN 的舊工具相容。
//
// 用 ECDSA P-256 而不是 RSA:金鑰產生跟簽章都快得多,對一份自簽、只給
// 使用者自己瀏覽器手動信任的憑證來說,安全性已經完全足夠,沒有理由
// 為了「看起來更正式」選一個慢好幾倍的演算法。
func GenerateSelfSignedCert(hosts []string, validFor time.Duration) (certPEM, keyPEM []byte, err error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generating certificate key: %w", err)
	}

	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return nil, nil, fmt.Errorf("generating certificate serial number: %w", err)
	}

	now := time.Now()
	template := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "GoNAS", Organization: []string{"GoNAS self-signed"}},
		// NotBefore 往回抓 5 分鐘餘裕,避免使用者裝置的時鐘只要慢一點點,
		// 瀏覽器就判定「憑證還沒生效」。
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.Add(validFor),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true, // 自己簽自己;標成 CA 是為了讓部分行動裝置的「手動信任這張憑證」流程能正常運作
	}

	for _, h := range hosts {
		if h == "" {
			continue
		}
		if ip := net.ParseIP(h); ip != nil {
			template.IPAddresses = append(template.IPAddresses, ip)
		} else {
			template.DNSNames = append(template.DNSNames, h)
		}
	}
	if len(template.IPAddresses) == 0 && len(template.DNSNames) == 0 {
		template.DNSNames = []string{"localhost"}
		template.IPAddresses = []net.IP{net.IPv4(127, 0, 0, 1)}
	}

	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		return nil, nil, fmt.Errorf("creating self-signed certificate: %w", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})

	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return nil, nil, fmt.Errorf("marshaling certificate private key: %w", err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	return certPEM, keyPEM, nil
}

// EnsureCertFiles 確保 certPath/keyPath 這一對檔案存在;兩個都已經存在
// 就什麼都不做,直接沿用既有憑證 —— 避免每次啟動都重新簽發一份新的,
// 讓瀏覽器每次都跳出「這個網站的憑證變了」的警告。任一個不存在就產生
// 一份全新的自簽憑證組並原子寫入兩個檔案。
func EnsureCertFiles(certPath, keyPath string, hosts []string, validFor time.Duration) error {
	_, certErr := os.Stat(certPath)
	_, keyErr := os.Stat(keyPath)
	if certErr == nil && keyErr == nil {
		return nil
	}

	certPEM, keyPEM, err := GenerateSelfSignedCert(hosts, validFor)
	if err != nil {
		return err
	}

	if err := writeFileAtomically(certPath, certPEM, 0o644); err != nil {
		return fmt.Errorf("writing certificate file: %w", err)
	}
	if err := writeFileAtomically(keyPath, keyPEM, 0o600); err != nil {
		return fmt.Errorf("writing certificate key file: %w", err)
	}
	return nil
}

// writeFileAtomically 是這個套件自己的原子寫入小工具(先寫暫存檔、
// chmod、rename),跟 internal/share.WriteConfigAtomically、
// internal/state 的 writeLocked 是同一個模式,但刻意不直接依賴那兩個
// 套件 —— internal/security 只依賴標準函式庫,不依賴專案裡其他業務邏輯
// 套件,方便之後如果要把這個套件單獨抽出去重用,不用拖著不相干的依賴。
func writeFileAtomically(path string, content []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("ensuring directory %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".gonas-tmp-*")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return fmt.Errorf("writing temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing temp file: %w", err)
	}
	if err := os.Chmod(tmpPath, perm); err != nil {
		return fmt.Errorf("chmod temp file: %w", err)
	}
	return os.Rename(tmpPath, path)
}
