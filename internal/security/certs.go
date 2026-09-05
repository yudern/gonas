package security

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sync"
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

// LoadCertExpiry 讀取 certPath 這份 PEM 憑證檔案,回傳它的 NotAfter
// (到期時間)。獨立成一個小函式,是因為「憑證何時到期」跟「怎麼產生
// 憑證」是兩件事——RenewCertIfNeeded 靠它決定要不要重簽,之後如果
// Web UI 想在「HTTPS 設定」頁面直接顯示「憑證還有幾天到期」,也能直接
// 重用,不用另外剖析一次憑證檔案。
func LoadCertExpiry(certPath string) (time.Time, error) {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return time.Time{}, fmt.Errorf("reading certificate file: %w", err)
	}
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return time.Time{}, fmt.Errorf("no PEM block found in certificate file %s", certPath)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return time.Time{}, fmt.Errorf("parsing certificate %s: %w", certPath, err)
	}
	return cert.NotAfter, nil
}

// RenewCertIfNeeded 是 EnsureCertFiles 的「續期」版本:如果 certPath/
// keyPath 這對檔案還不存在,行為完全等同 EnsureCertFiles(產生一份全新
// 自簽憑證);如果已經存在,額外檢查現有憑證是不是快到期了——
// 「快到期」的定義是 now.Add(renewBefore) 已經超過憑證的 NotAfter,也
// 就是說在接下來 renewBefore 這段時間裡憑證就會過期。是的話用同一組
// hosts/validFor 重新簽發、原子覆寫掉舊的 cert/key 檔案;還沒到期就
// 什麼都不做,回傳 renewed=false。
//
// now 抽成參數(而不是函式內部呼叫 time.Now())是為了讓測試能餵一個
// 「已經超過到期日」的固定時間點,不用真的產生一份效期只有幾毫秒的
// 憑證再等它過期——跟這個專案其他地方(internal/backup 的
// nextRunDelay)把「現在時間」抽成參數方便測試是同一個理由。
//
// renewed=true 但 err!=nil 的組合不會發生:任何一步失敗都直接回傳
// renewed=false 跟對應的錯誤,呼叫端(CertRenewer)不需要處理「重簽
// 一半」的中間狀態——舊的、還沒過期(或已經過期但還能用)的 cert/key
// 檔案在失敗時完全不會被動到,因為 GenerateSelfSignedCert 產生失敗時
// 根本還沒開始寫檔案,writeFileAtomically 本身又是先寫暫存檔再
// rename,不會留下寫一半的檔案。
func RenewCertIfNeeded(certPath, keyPath string, hosts []string, validFor, renewBefore time.Duration, now time.Time) (renewed bool, err error) {
	_, certErr := os.Stat(certPath)
	_, keyErr := os.Stat(keyPath)
	if certErr != nil || keyErr != nil {
		if err := EnsureCertFiles(certPath, keyPath, hosts, validFor); err != nil {
			return false, err
		}
		return true, nil
	}

	expiry, err := LoadCertExpiry(certPath)
	if err != nil {
		return false, err
	}
	if !now.Add(renewBefore).After(expiry) {
		// 還沒進入續期門檻,現有憑證繼續用。
		return false, nil
	}

	certPEM, keyPEM, err := GenerateSelfSignedCert(hosts, validFor)
	if err != nil {
		return false, err
	}
	if err := writeFileAtomically(certPath, certPEM, 0o644); err != nil {
		return false, fmt.Errorf("writing renewed certificate file: %w", err)
	}
	if err := writeFileAtomically(keyPath, keyPEM, 0o600); err != nil {
		return false, fmt.Errorf("writing renewed certificate key file: %w", err)
	}
	return true, nil
}

// CertStore 讓一個已經在監聽的 TLS listener 能拿到「目前最新」的憑證,
// 不需要重啟才能套用 RenewCertIfNeeded 剛剛重新簽發的新憑證——
// tls.Config.GetCertificate 這個回呼在每次 TLS 交握時都會被呼叫,
// CertStore 在這裡檢查 cert/key 檔案的修改時間有沒有變,變了才真的
// 重新讀檔、解析,沒變就直接回傳快取的結果,避免每次 TLS 交握都做一次
// 沒必要的磁碟 I/O。
//
// 這是 Go 生態圈裡處理「TLS 憑證要能不重啟熱更新」的標準寫法(不少
// 知名的 Go HTTP 伺服器/反向代理都是這樣做),沒有用任何第三方套件。
type CertStore struct {
	certPath, keyPath string

	mu                      sync.Mutex
	cert                    *tls.Certificate
	certModTime, keyModTime time.Time
}

// NewCertStore 建立一個指向 certPath/keyPath 的 CertStore。這個呼叫本身
// 不會立刻讀檔——第一次真正的 TLS 交握呼叫 GetCertificate 時才會讀,
// 讀取失敗會回傳錯誤讓那次交握失敗,而不是讓 NewCertStore 也需要回傳
// error(NewCertStore 呼叫的當下,檔案理論上還沒被使用者要求的 HTTPS
// 開關流程準備好也是合理狀態,不該讓建立 CertStore 這個動作本身失敗)。
func NewCertStore(certPath, keyPath string) *CertStore {
	return &CertStore{certPath: certPath, keyPath: keyPath}
}

// GetCertificate 實作 tls.Config.GetCertificate 需要的簽名,可以直接
// 指定給 tls.Config{GetCertificate: store.GetCertificate}。
func (c *CertStore) GetCertificate(_ *tls.ClientHelloInfo) (*tls.Certificate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	certInfo, err := os.Stat(c.certPath)
	if err != nil {
		return nil, fmt.Errorf("stat certificate file: %w", err)
	}
	keyInfo, err := os.Stat(c.keyPath)
	if err != nil {
		return nil, fmt.Errorf("stat certificate key file: %w", err)
	}

	if c.cert != nil && certInfo.ModTime().Equal(c.certModTime) && keyInfo.ModTime().Equal(c.keyModTime) {
		return c.cert, nil
	}

	pair, err := tls.LoadX509KeyPair(c.certPath, c.keyPath)
	if err != nil {
		return nil, fmt.Errorf("loading certificate/key pair: %w", err)
	}

	c.cert = &pair
	c.certModTime = certInfo.ModTime()
	c.keyModTime = keyInfo.ModTime()
	return c.cert, nil
}

// CertRenewer 背景週期性檢查一份自簽憑證是不是快到期,快到期就用
// RenewCertIfNeeded 重新簽發——搭配 CertStore 的熱重載,整個「憑證
// 快過期了、自動換一張新的」流程完全不需要重啟 gonasd。跟
// internal/backup.JobScheduler、internal/storage.Scheduler 是同樣的
// 「獨立 goroutine + ticker,Stop() 保證真的結束」骨架,刻意不共用
// 同一個型別——理由跟那兩個套件開頭的說明一致:各自演化不互相牽動。
type CertRenewer struct {
	logger *slog.Logger
	cancel context.CancelFunc
	done   chan struct{}
}

// NewCertRenewer 建立續期器但不會立刻開始跑,需呼叫 Start。
func NewCertRenewer(logger *slog.Logger) *CertRenewer {
	return &CertRenewer{logger: logger}
}

// Start 依 checkInterval 週期性檢查憑證是否需要續期。啟動當下就會先
// 檢查一次(而不是等第一個 checkInterval 過去才檢查)——如果 gonasd
// 這次重啟前已經停機了一段時間、憑證早就超過續期門檻甚至已經過期,
// 使用者不該還要多等一個完整的 checkInterval 才會被自動修好。
func (r *CertRenewer) Start(ctx context.Context, checkInterval time.Duration, certPath, keyPath string, hosts []string, validFor, renewBefore time.Duration) {
	ctx, cancel := context.WithCancel(ctx)
	r.cancel = cancel
	r.done = make(chan struct{})

	check := func() {
		renewed, err := RenewCertIfNeeded(certPath, keyPath, hosts, validFor, renewBefore, time.Now())
		if err != nil {
			if r.logger != nil {
				r.logger.Error("checking/renewing self-signed TLS certificate failed", "certPath", certPath, "err", err)
			}
			return
		}
		if renewed && r.logger != nil {
			r.logger.Info("renewed self-signed TLS certificate", "certPath", certPath, "validFor", validFor)
		}
	}

	go func() {
		defer close(r.done)
		check()

		ticker := time.NewTicker(checkInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				check()
			}
		}
	}()
}

// Stop 讓續期 goroutine 結束,並等它真的結束才回傳。在還沒呼叫過
// Start 的情況下是安全的 no-op。
func (r *CertRenewer) Stop() {
	if r.cancel == nil {
		return
	}
	r.cancel()
	<-r.done
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
