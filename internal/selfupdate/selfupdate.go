// Package selfupdate 讓 gonasd 能自己檢查、下載、驗證並套用新版本的
// 執行檔,不需要使用者手動 SSH 進機器重新跑一次 install.sh。
//
// 設計上刻意保守,理由有兩個:第一,GoNAS 是管理使用者資料陣列的 NAS
// 軟體,自我更新這件事本質上就是「用網路上下載回來的東西取代自己正在
// 執行的程式」,風險比一般應用程式的自動更新更高——資料陣列、Docker
// 容器都還掛著,一個壞掉的更新如果讓 gonasd 起不來,使用者要面對的不是
// 「重開一個 App」這麼簡單,而是「NAS 的管理介面整個打不開」;第二,
// 跟這個專案其他所有網路呼叫一樣,自我更新的來源(下面說的
// ManifestURL)完全由使用者自己設定,GoNAS 不會內建任何預設的更新
// 伺服器網址、更不會在使用者不知情的情況下自動打電話回家——沒有設定
// 就完全不會發出任何網路請求(見 state.UpdateConfig 的說明)。
//
// 檢查(取得 Manifest、比對版本)、套用(下載、驗證、原子替換執行檔)
// 分成兩個獨立的動作:handleSystemUpdateCheck 只做前者,回應快、沒有
// 副作用;handleSystemUpdateApply 才會真的動到磁碟上的執行檔,一律要
// 管理者(RoleAdmin)明確觸發,不會有任何自動套用的路徑——是否要升級,
// 永遠是使用者自己按下按鈕的決定,不是背景 goroutine 幫使用者決定的。
//
// 更新來源(Manifest)是一份使用者自己架設/信任的靜態 JSON 檔案,格式:
//
//	{
//	  "version": "v0.4.0",
//	  "notes": "這個版本修了...",
//	  "assets": {
//	    "linux-amd64": {"url": "https://.../gonasd-linux-amd64", "sha256": "<hex>"},
//	    "linux-arm64": {"url": "https://.../gonasd-linux-arm64", "sha256": "<hex>"}
//	  }
//	}
//
// 資產鍵值是 "<runtime.GOOS>-<runtime.GOARCH>",跟
// internal/version.Info 裡的 GoOS/GoArch 欄位、既有 release tarball
// 命名慣例(gonas-<version>-linux-<arch>.tar.gz)保持一致。
//
// 全部只用標準函式庫(net/http、crypto/sha256、encoding/json、
// syscall),沒有第三方依賴,跟這個專案一貫的取捨一致。
package selfupdate

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/bng147/gonas/internal/safe"
)

// Asset 是 Manifest 裡某個平台(GOOS-GOARCH)對應的下載資訊。
type Asset struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"` // hex 編碼,大小寫不拘
}

// Manifest 描述目前「最新」的 GoNAS 版本,以及各平台的下載資訊。
type Manifest struct {
	Version string           `json:"version"`
	Notes   string           `json:"notes,omitempty"`
	Assets  map[string]Asset `json:"assets"`
}

// AssetFor 依 goos-goarch 找出對應的下載資訊,找不到就回傳明確的錯誤
// (例如使用者只在 Manifest 裡放了 linux-amd64,卻拿一台 arm64 機器來
// 檢查更新),而不是讓呼叫端拿到零值 Asset 去下載一個空 URL。
func (m Manifest) AssetFor(goos, goarch string) (Asset, error) {
	key := goos + "-" + goarch
	asset, ok := m.Assets[key]
	if !ok {
		return Asset{}, fmt.Errorf("selfupdate: manifest has no asset for platform %q", key)
	}
	if asset.URL == "" {
		return Asset{}, fmt.Errorf("selfupdate: asset for platform %q has an empty url", key)
	}
	return asset, nil
}

// maxManifestBytes 是願意讀取的 Manifest 檔案大小上限。這是一份純文字
// 的小型 JSON,幾 KB 就綽綽有餘,設一個寬裕但有限的上限(1 MiB,跟
// internal/api.maxRequestBodyBytes 的思路一致)只是為了擋掉「使用者
// 不小心把網址填成一個超大檔案」或惡意回應撐爆記憶體的情況。
const maxManifestBytes = 1 << 20 // 1 MiB

// ManifestPublicKeyHex 是用來驗證 manifest 數位簽章的 ed25519 公鑰
// (hex 編碼,32 bytes = 64 個 hex 字元)。**預設空字串**,透過建置時的
// ldflags 注入,例如:
//
//	go build -ldflags "-X github.com/bng147/gonas/internal/selfupdate.ManifestPublicKeyHex=<hex>"
//
// 第三十二輪(全鏈路覆核修法 ④)的背景:原本自我更新只比對 manifest 裡
// 的 SHA256(完整性),沒有任何簽章(真實性)——信任完全落在「manifest
// 的 HTTPS 來源沒被竄改」上。誰能竄改那個來源,就能同時改掉 URL 跟
// SHA256、推一個以 root 執行的任意執行檔。加上這一層之後:
//
//   - 有設定公鑰(非空):FetchManifest 會去抓 <manifestURL>.sig 這個
//     detached 簽章(base64 的 ed25519 簽章),用公鑰驗證它蓋在「manifest
//     原始位元組」上——驗不過就拒絕,fail-closed。這樣就算 manifest 來源
//     被完全掌控,沒有對應私鑰也偽造不出有效簽章。
//   - 沒設定公鑰(預設空字串):維持原本只有 SHA256 的行為(向後相容,
//     不會讓既有、沒在簽章的部署突然壞掉),但 Checker 會記一筆 log 提醒
//     「這次更新沒有經過簽章驗證」。
//
// 簽章那一層驗證的是「manifest(含每個 asset 的 URL 與 SHA256)確實由
// 持有私鑰的人發佈」;下載執行檔本身仍照舊用 manifest 裡的 SHA256 驗證
// 完整性——兩層各司其職。
var ManifestPublicKeyHex string

// manifestSignatureSuffix 是 detached 簽章相對於 manifest URL 的固定後綴,
// 沿用 Debian「SHA256SUMS + SHA256SUMS.sign」那種 detached 簽章的慣例。
const manifestSignatureSuffix = ".sig"

// maxSignatureBytes 是願意讀取的簽章檔大小上限——base64 的 ed25519 簽章
// 只有 88 個字元,給到 1 KiB 綽綽有餘,擋掉惡意的超大回應。
const maxSignatureBytes = 1024

// verifyManifestSignature 在有設定公鑰時,抓 detached 簽章並驗證它蓋在
// manifest 原始位元組上。沒設定公鑰時直接回 nil(由呼叫端決定要不要記
// 「未簽章」的提醒)。
func verifyManifestSignature(ctx context.Context, client *http.Client, manifestURL string, manifestBody []byte) error {
	if strings.TrimSpace(ManifestPublicKeyHex) == "" {
		return nil // 沒設定公鑰:跳過簽章驗證(向後相容)
	}
	pub, err := hex.DecodeString(strings.TrimSpace(ManifestPublicKeyHex))
	if err != nil || len(pub) != ed25519.PublicKeySize {
		// 這是建置期就該設對的東西,設錯了寧可硬失敗、也不要退回「不驗章」
		// ——不然一個手滑打錯的公鑰會靜默地把整層防護關掉。
		return fmt.Errorf("selfupdate: ManifestPublicKeyHex is not a valid %d-byte ed25519 public key", ed25519.PublicKeySize)
	}

	sigURL := manifestURL + manifestSignatureSuffix
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sigURL, nil)
	if err != nil {
		return fmt.Errorf("selfupdate: building signature request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("selfupdate: fetching manifest signature %s: %w", sigURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("selfupdate: manifest signature server returned %s (a public key is configured, so a valid %s is required)", resp.Status, manifestSignatureSuffix)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxSignatureBytes+1))
	if err != nil {
		return fmt.Errorf("selfupdate: reading manifest signature: %w", err)
	}
	if len(raw) > maxSignatureBytes {
		return fmt.Errorf("selfupdate: manifest signature exceeds %d bytes", maxSignatureBytes)
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		return fmt.Errorf("selfupdate: manifest signature is not valid base64: %w", err)
	}
	if !ed25519.Verify(ed25519.PublicKey(pub), manifestBody, sig) {
		return fmt.Errorf("selfupdate: manifest signature verification FAILED — refusing this update (the manifest may have been tampered with, or was not signed by the configured key)")
	}
	return nil
}

// ManifestSignatureEnforced 回報這個建置有沒有內嵌公鑰(也就是自我更新
// 會不會強制驗簽)。給呼叫端拿來決定要記「已驗簽」還是「未簽章」的 log。
func ManifestSignatureEnforced() bool {
	return strings.TrimSpace(ManifestPublicKeyHex) != ""
}

// FetchManifest 用 client 對 manifestURL 發一個 GET 請求並解析回應的
// JSON。client 是呼叫端注入的(而不是用 http.DefaultClient),方便測試
// 控制逾時/傳輸行為,也讓正式呼叫端能套用專案一致的逾時設定。
// requireHTTPS 強制更新來源用 https。第三十輪覆核(資深安全工程師)指出:
// 沒有內嵌公鑰時 manifest 未驗簽,誰能竄改回應就能同時改 URL 與 sha256,
// 套用時以 root 覆蓋自身並重啟 —— 所以至少要擋掉明文 http(避免區網 MITM
// /降級),把「傳輸未受保護」這個最容易被踩的破口關掉。內嵌公鑰驗簽仍是
// 更強的一層(見 verifyManifestSignature),兩者並存。
//
// 例外:明文 http 指向 loopback(localhost / 127.0.0.1 / ::1)一律放行 ——
// 同一台機器上的流量沒有 MITM 風險,而且本機測試/自架鏡像常這樣用。真正要
// 擋的是「跨網路的明文來源」。
func requireHTTPS(rawurl, what string) error {
	u, err := url.Parse(strings.TrimSpace(rawurl))
	if err != nil {
		return fmt.Errorf("selfupdate: %s is not a valid URL: %w", what, err)
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return nil
	case "http":
		switch u.Hostname() {
		case "localhost", "127.0.0.1", "::1":
			return nil
		}
	}
	return fmt.Errorf("selfupdate: %s must use https:// (refusing plaintext/insecure URL)", what)
}

// secureRedirect 是給自我更新的 http.Client 用的 CheckRedirect 政策。
// 第三十輪覆核只擋了「初始 URL 必須是 https」,但 requireHTTPS 只看得到
// 呼叫端傳進來的第一個網址——如果那個 https 伺服器回 301/302 把我們導去
// http://evil/...,Go 的 http.Client 預設會乖乖跟著跳,傳輸就悄悄降級成
// 明文,requireHTTPS 完全被繞過。傳輸層的信任是「逐跳」的,所以這裡在
// 每一次重導向都重新套一次 requireHTTPS:任何一跳想降級到非 https(且
// 非 loopback)就直接中止,fail-closed。沿用 Go 預設「最多 10 跳」的上限。
func secureRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return fmt.Errorf("selfupdate: stopped after 10 redirects")
	}
	return requireHTTPS(req.URL.String(), "redirect target")
}

// NewHTTPClient 建立一個已經套用自我更新安全重導向政策(secureRedirect)
// 的 http.Client。呼叫端(internal/api)應該一律用這個建構子,而不是自己
// new 一個 &http.Client{}——否則 requireHTTPS 的保護會在第一次重導向之後
// 失效(見 secureRedirect 的說明)。
func NewHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:       timeout,
		CheckRedirect: secureRedirect,
	}
}

func FetchManifest(ctx context.Context, client *http.Client, manifestURL string) (Manifest, error) {
	if err := requireHTTPS(manifestURL, "manifest URL"); err != nil {
		return Manifest{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, manifestURL, nil)
	if err != nil {
		return Manifest{}, fmt.Errorf("selfupdate: building manifest request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return Manifest{}, fmt.Errorf("selfupdate: fetching manifest: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return Manifest{}, fmt.Errorf("selfupdate: manifest server returned %s", resp.Status)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxManifestBytes+1))
	if err != nil {
		return Manifest{}, fmt.Errorf("selfupdate: reading manifest response: %w", err)
	}
	if len(body) > maxManifestBytes {
		return Manifest{}, fmt.Errorf("selfupdate: manifest response exceeds %d bytes", maxManifestBytes)
	}

	// 第三十二輪:有內嵌公鑰時,先驗證 manifest 的 detached 簽章,驗不過
	// 就拒絕——擋在 json.Unmarshal 跟後續下載之前,一個被竄改的 manifest
	// 連解析都不該解析。沒設定公鑰時這裡是 no-op(向後相容)。
	if err := verifyManifestSignature(ctx, client, manifestURL, body); err != nil {
		return Manifest{}, err
	}

	var m Manifest
	if err := json.Unmarshal(body, &m); err != nil {
		return Manifest{}, fmt.Errorf("selfupdate: parsing manifest json: %w", err)
	}
	if strings.TrimSpace(m.Version) == "" {
		return Manifest{}, fmt.Errorf("selfupdate: manifest is missing a version field")
	}
	return m, nil
}

// semverPrefix 抓出版本字串開頭的 vMAJOR.MINOR.PATCH(v 可有可無),
// 忽略後面 `git describe --tags` 常見的 "-<N>-g<hash>"、"-dirty" 這類
// 後綴——這樣 "v0.4.0"、"v0.4.0-3-gabc1234"、"v0.4.0-dirty" 都能正確
// 取出 (0,4,0) 來比較,只有真正的三段數字前綴解析失敗(例如 "dev"、
// 完全沒有標籤過的原始碼建置)才會被視為「無法比較」。
var semverPrefix = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)`)

// ParseVersion 解析版本字串的 MAJOR.MINOR.PATCH 前綴,ok=false 代表
// 無法解析(例如開發模式下的 "dev" 版本字串)。
func ParseVersion(v string) (major, minor, patch int, ok bool) {
	m := semverPrefix.FindStringSubmatch(v)
	if m == nil {
		return 0, 0, 0, false
	}
	major, _ = strconv.Atoi(m[1])
	minor, _ = strconv.Atoi(m[2])
	patch, _ = strconv.Atoi(m[3])
	return major, minor, patch, true
}

// IsNewer 回報 candidate 是不是比 current 新的版本。comparable=false
// 代表其中至少一個版本字串解析不出 MAJOR.MINOR.PATCH(最常見的情況是
// current 是開發模式下的 "dev")——這種情況下 IsNewer 保守地回傳
// newer=false,而不是亂猜一個答案:自我更新功能刻意只服務「從正式
// release tarball 安裝、Version 是真正 git tag 出來的」這種情境,
// 直接從原始碼 go run/go build(Version 落回 "dev")的開發環境不會被
// (錯誤地)提示「有新版本」。
func IsNewer(current, candidate string) (newer bool, comparable bool) {
	cMajor, cMinor, cPatch, cOK := ParseVersion(current)
	nMajor, nMinor, nPatch, nOK := ParseVersion(candidate)
	if !cOK || !nOK {
		return false, false
	}
	if nMajor != cMajor {
		return nMajor > cMajor, true
	}
	if nMinor != cMinor {
		return nMinor > cMinor, true
	}
	return nPatch > cPatch, true
}

// maxDownloadBytes 是願意下載的執行檔大小上限。GoNAS 執行檔本身(靜態
// 連結、trimpath)目前是十幾 MB 等級,200 MiB 給了非常寬裕的成長空間,
// 同時還是擋得住「URL 設定錯誤指到一個超大檔案」或惡意回應撐爆磁碟/
// 記憶體的情況。
const maxDownloadBytes = 200 << 20 // 200 MiB

// DownloadAndVerify 下載 asset.URL 的內容到 destDir 底下的一個暫存檔,
// 一邊下載一邊計算 SHA-256,下載完成後跟 asset.SHA256 比對。刻意要求
// 呼叫端指定 destDir(而不是用系統預設的暫存目錄),是因為 ApplyUpdate
// 最後要把這個暫存檔 rename 成正式的執行檔——rename(2) 只有在來源/
// 目的地在同一個檔案系統時才是原子操作,把暫存檔案直接生在執行檔所在
// 目錄底下,能保證後面那個 rename 一定是同檔案系統內的操作,不會退化成
// 「複製+刪除」這種非原子、中途失敗會留下半個檔案的路徑。
//
// 驗證失敗或任何步驟出錯時,暫存檔案會被清乾淨,不會留下未驗證、
// 半下載的檔案佔用磁碟空間或被誤用。
func DownloadAndVerify(ctx context.Context, client *http.Client, asset Asset, destDir string) (tempPath string, err error) {
	return DownloadAndVerifyWithProgress(ctx, client, asset, destDir, nil)
}

// DownloadAndVerifyWithProgress 跟 DownloadAndVerify 一樣,另外在下載過程中
// 呼叫 onProgress(已下載位元組, 總位元組;伺服器沒給長度時為 -1)。第六十五輪
// 加上,給 Web UI 顯示線上更新的下載進度。onProgress 可為 nil。
func DownloadAndVerifyWithProgress(ctx context.Context, client *http.Client, asset Asset, destDir string, onProgress func(done, total int64)) (tempPath string, err error) {
	if err := requireHTTPS(asset.URL, "asset URL"); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.URL, nil)
	if err != nil {
		return "", fmt.Errorf("selfupdate: building download request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("selfupdate: downloading update: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("selfupdate: download server returned %s", resp.Status)
	}

	if err := os.MkdirAll(destDir, 0o750); err != nil {
		return "", fmt.Errorf("selfupdate: ensuring destination directory: %w", err)
	}

	tmp, err := os.CreateTemp(destDir, ".gonasd-update-*")
	if err != nil {
		return "", fmt.Errorf("selfupdate: creating temp file: %w", err)
	}
	// createdPath 特意跟具名回傳值 tempPath 分開存放:底下任何一個失敗
	// 路徑執行 `return "", err` 時,Go 會先把具名回傳值 tempPath 設成
	// ""、才觸發這個 defer——如果 defer 裡直接讀 tempPath,讀到的已經是
	// 空字串,實際留在磁碟上的暫存檔就永遠不會被 os.Remove 清掉。用一個
	// 不受具名回傳值影響的獨立變數,才能保證失敗時真的清得掉。
	createdPath := tmp.Name()
	// 任何一步失敗都清掉暫存檔——success 路徑會在函式最後把 cleanup
	// 取消掉(succeeded=true),避免下載成功的檔案被自己的 defer 誤刪。
	succeeded := false
	defer func() {
		if !succeeded {
			tmp.Close()
			os.Remove(createdPath)
		}
	}()

	hasher := sha256.New()
	limited := io.LimitReader(resp.Body, maxDownloadBytes+1)
	var dst io.Writer = io.MultiWriter(tmp, hasher)
	if onProgress != nil {
		dst = &progressWriter{w: dst, total: resp.ContentLength, fn: onProgress}
		onProgress(0, resp.ContentLength)
	}
	written, err := io.Copy(dst, limited)
	if err != nil {
		return "", fmt.Errorf("selfupdate: writing downloaded update: %w", err)
	}
	if written > maxDownloadBytes {
		return "", fmt.Errorf("selfupdate: downloaded update exceeds %d bytes", maxDownloadBytes)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("selfupdate: closing downloaded update file: %w", err)
	}

	gotSum := hex.EncodeToString(hasher.Sum(nil))
	wantSum := strings.ToLower(strings.TrimSpace(asset.SHA256))
	if !strings.EqualFold(gotSum, wantSum) {
		return "", fmt.Errorf("selfupdate: checksum mismatch: downloaded file sha256 %s, manifest says %s", gotSum, wantSum)
	}

	if err := os.Chmod(createdPath, 0o755); err != nil {
		return "", fmt.Errorf("selfupdate: making downloaded update executable: %w", err)
	}

	succeeded = true
	return createdPath, nil
}

// elfMachineForGoarch 把 Go 的 GOARCH 對應到 ELF 檔頭 e_machine 欄位的值
// (ELF spec 的 EM_* 常數)。只列 GoNAS 實際會出的三種 Linux 架構——這是
// 用來擋「離線上傳更新」時傳錯架構的執行檔(例如拿 arm64 的檔案上傳到 amd64
// 的機器),不是要支援任意平台。找不到對應(理論上不會發生,因為
// runtime.GOARCH 一定是編這份 gonasd 時的其中一種)時回 (0,false),呼叫端
// 會退成「只檢查是不是 ELF」而不強制比對架構,不會誤擋。
func elfMachineForGoarch(goarch string) (uint16, bool) {
	switch goarch {
	case "amd64":
		return 0x3E, true // EM_X86_64
	case "arm64":
		return 0xB7, true // EM_AARCH64
	case "arm":
		return 0x28, true // EM_ARM
	default:
		return 0, false
	}
}

// VerifyExecutableForHost 檢查 path 指到的檔案「看起來是不是這台機器平台的
// 可執行檔」——目前只支援 Linux ELF(GoNAS 的部署平台)。這是「離線上傳
// 更新」的第一道便宜關卡:在真的把它換成正在執行的 daemon 之前,先用檔頭
// 擋掉最常見的兩種災難——傳錯架構(arm64 的檔案傳到 amd64 機器)、傳到一個
// 根本不是執行檔的東西(整個 tar.gz、文字檔、半個檔案)。這一步只讀檔頭
// 幾個 byte、不執行任何東西,所以很安全也很快;真正「能不能跑起來」的確認
// 由呼叫端再跑一次 `--version` 把關(見 internal/api.handleSystemUpdateUpload)。
//
// 非 Linux(理論上 GoNAS 不會部署在別的 OS 上)時只做「檔案存在且非空」的
// 最低限度檢查,不去猜別的可執行檔格式。
func VerifyExecutableForHost(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("selfupdate: opening uploaded file: %w", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("selfupdate: reading uploaded file info: %w", err)
	}
	if info.Size() == 0 {
		return fmt.Errorf("selfupdate: uploaded file is empty")
	}

	if runtime.GOOS != "linux" {
		return nil // 非 Linux:不猜格式,交給後續 --version 把關
	}

	// 讀 ELF 檔頭前 20 個 byte 就夠拿到 magic + e_machine。
	var hdr [20]byte
	if _, err := io.ReadFull(f, hdr[:]); err != nil {
		return fmt.Errorf("selfupdate: uploaded file is too small to be a valid executable")
	}
	// ELF magic: 0x7F 'E' 'L' 'F'
	if hdr[0] != 0x7F || hdr[1] != 'E' || hdr[2] != 'L' || hdr[3] != 'F' {
		return fmt.Errorf("selfupdate: uploaded file is not a Linux executable (bad ELF magic) — make sure you uploaded the gonasd binary itself, not a .tar.gz or a text file")
	}
	// e_machine 在 offset 18(2 bytes)。EI_DATA(offset 5)=1 表示小端序,
	// GoNAS 的三種目標架構(x86_64/aarch64/arm)在 Linux 上都是小端序。
	machine := uint16(hdr[18]) | uint16(hdr[19])<<8
	if want, ok := elfMachineForGoarch(runtime.GOARCH); ok && machine != want {
		return fmt.Errorf("selfupdate: uploaded binary is for a different CPU architecture (this machine is %s) — upload the gonasd build that matches this machine", runtime.GOARCH)
	}
	return nil
}

// ApplyUpdate 用 verifiedBinaryPath(已經通過 DownloadAndVerify 驗證過
// 的檔案,必須跟 currentExecPath 在同一個檔案系統上)原子取代
// currentExecPath 目前的內容。取代之前會先盡力把目前的執行檔備份成
// "<currentExecPath>.previous"——這一步失敗不會擋下整個更新(記一筆
// log 就好),但成功的話,萬一新版本起不來,使用者手動把 .previous
// 檔案改回原本的檔名就能復原,不需要重新跑一次 install.sh。
//
// 回傳的 backupPath 是實際備份到的路徑(備份失敗時是空字串),純粹給
// 呼叫端記 log/顯示用。
func ApplyUpdate(currentExecPath, verifiedBinaryPath string) (backupPath string, err error) {
	backupPath = currentExecPath + ".previous"
	if err := os.Rename(currentExecPath, backupPath); err != nil {
		// 備份失敗不是致命錯誤(例如檔案系統權限問題、或這是全新安裝
		// currentExecPath 剛好還不存在這種邊界情況),繼續往下做,只是
		// 沒有復原點可用。
		backupPath = ""
	}

	if err := os.Rename(verifiedBinaryPath, currentExecPath); err != nil {
		// 正式替換失敗了——如果剛才備份成功,盡力把備份的檔案搬回來,
		// 讓 gonasd 至少還能繼續用舊版本跑,不會兩邊都沒有可執行檔案。
		if backupPath != "" {
			_ = os.Rename(backupPath, currentExecPath)
		}
		return backupPath, fmt.Errorf("selfupdate: replacing executable: %w", err)
	}

	return backupPath, nil
}

// RollbackToBackup 是 ApplyUpdate 的反向操作:把 "<currentExecPath>.previous"
// 换回 currentExecPath,讓使用者在套用新版本之後發現有問題時,能一鍵
// 退回上一個版本,不需要 SSH 進機器手動搬檔案(見
// internal/api.handleSystemUpdateRollback)。
//
// 找不到 .previous 備份檔案時直接回傳錯誤——沒有備份就沒有東西可以
// 復原,呼叫端(Web UI)應該提前用 GET /api/v1/system/update 判斷有沒有
// 備份可用,只在有的時候才顯示「復原」按鈕,但這裡仍然防禦性地檢查,
// 不假設呼叫端一定做對。
//
// 目前(有問題的新版本)不會直接被覆蓋掉、而是先搬到一個帶時間戳記的
// 檔名("<currentExecPath>.rolled-back-<unix秒>")留著,而不是直接刪除
// ——這樣萬一 .previous 本身也有問題(例如兩個版本都壞、或使用者
// 復原錯了方向),剛剛復原前的那個檔案還找得回來,不會真的無路可退。
// 這跟 ApplyUpdate 保留 .previous 備份是同一個「多留一手」的思路。
func RollbackToBackup(currentExecPath string) (rolledBackFromPath string, err error) {
	backupPath := currentExecPath + ".previous"
	if _, statErr := os.Stat(backupPath); statErr != nil {
		return "", fmt.Errorf("selfupdate: no backup found at %s: %w", backupPath, statErr)
	}

	rolledBackFromPath = fmt.Sprintf("%s.rolled-back-%d", currentExecPath, time.Now().Unix())
	if err := os.Rename(currentExecPath, rolledBackFromPath); err != nil {
		return "", fmt.Errorf("selfupdate: moving current executable aside: %w", err)
	}

	if err := os.Rename(backupPath, currentExecPath); err != nil {
		// 復原失敗——盡力把原本的執行檔搬回來,不要讓 gonasd 一個可執行
		// 檔案都不剩。
		_ = os.Rename(rolledBackFromPath, currentExecPath)
		return "", fmt.Errorf("selfupdate: restoring backup: %w", err)
	}

	return rolledBackFromPath, nil
}

// Reexec 用新的執行檔內容重新啟動目前這個 process——不是「先結束、
// 等外部東西(systemd/Docker)再啟動一個新的」,而是透過 POSIX
// exec(2) 系統呼叫直接在同一個 PID 上換掉整個程式映像檔。這是刻意的
// 選擇:GoNAS 同時支援「systemd 服務」「Docker 容器」「直接在終端機
// 執行」三種部署方式(見 README「部署」一節),如果自我更新依賴
// systemd 的 Restart=on-failure 或 Docker 的重啟策略,在「直接執行」
// 這種部署方式下更新完就會停在那裡、沒有東西幫忙拉起新的程序;用
// exec(2) 換照片的方式完全不需要知道自己被誰監督,三種部署方式都能
// 正常運作。
//
// exec(2) 對於已經設定 close-on-exec 的檔案描述符(Go 的 net.Listener
// 預設就是)會在替換程式映像檔的同時自動關閉,所以監聽中的 port 會在
// 新程式的 main() 執行前就已經釋放,新程序重新綁定同一個 port 不會有
// 「address already in use」的競爭問題——呼叫端只需要在呼叫 Reexec
// 之前,自己決定要不要先做 http.Server.Shutdown 讓進行中的請求(尤其
// 是觸發這次更新的那個 HTTP 請求本身)有機會先寫完回應。
//
// execPath 必須是已經被 ApplyUpdate 換成新版本內容之後的路徑。
func Reexec(execPath string, args, env []string) error {
	absPath, err := filepath.Abs(execPath)
	if err != nil {
		return fmt.Errorf("selfupdate: resolving executable path: %w", err)
	}
	return syscall.Exec(absPath, args, env)
}

// CheckResult 是背景檢查器一次檢查的結果,快取在 Checker 裡供 API 層
// 隨時讀取(不需要每次 GET /api/v1/system/update 都真的打一次外部
// 網路請求)。
type CheckResult struct {
	CheckedAt       time.Time
	LatestVersion   string
	UpdateAvailable bool
	Notes           string
	Err             error
}

// Checker 背景週期性檢查是否有新版本可用。跟
// internal/security.CertRenewer、internal/backup.JobScheduler 是同樣的
// 「獨立 goroutine + ticker,Stop() 保證真的結束」骨架,一貫的理由:
// 各自演化不互相牽動,不用共用同一個排程型別。
//
// 跟 CertRenewer 不一樣的地方(也是自我更新這個功能最重要的安全/隱私
// 屬性):getManifestURL 回傳空字串時,check 完全跳過、不發任何網路
// 請求——這讓「使用者還沒設定更新來源」這件事,從一開始就等於「這個
// 功能完全關閉」,而不是「用某個內建預設值悄悄檢查」。getManifestURL
// 每次檢查前都重新呼叫(而不是在 Start 呼叫當下讀一次快取住),所以
// 使用者透過 Web UI 改變/清空更新來源網址,不需要重啟 gonasd,最晚
// 下一次檢查週期就會反映新設定。
type Checker struct {
	logger *slog.Logger
	cancel context.CancelFunc
	done   chan struct{}

	mu     sync.Mutex
	latest CheckResult
}

// NewChecker 建立檢查器但不會立刻開始跑,需呼叫 Start。
func NewChecker(logger *slog.Logger) *Checker {
	return &Checker{logger: logger}
}

// Latest 回傳最近一次檢查的結果。還沒檢查過(Start 還沒被呼叫、或
// 呼叫過但這是第一次 tick 之前)回傳零值 CheckResult{}(CheckedAt 是
// 零時間值)。
func (c *Checker) Latest() CheckResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.latest
}

// CheckNow 立刻對 manifestURL 執行一次檢查,並把結果寫進 Latest() 之後
// 會讀到的快取——不像 Start 內部的背景檢查迴圈,這裡不會因為
// manifestURL 是空字串就悄悄跳過:呼叫端(handleSystemUpdateCheck)會在
// 呼叫前自己先確認 manifestURL 有設定,這裡收到空字串代表呼叫端的邏輯
// 有誤,直接讓 FetchManifest 對空網址發請求失敗、把錯誤如實回報,而不是
// 在這一層悄悄吃掉。
//
// 抽成一個獨立、可以在背景檢查週期之外被呼叫的方法,是為了讓
// handleSystemUpdateCheck(使用者在 Web UI 按下「立即檢查」)不需要等
// 到下一次 ticker 觸發,同時又能跟背景週期性檢查共用同一段「打
// Manifest、比較版本、寫回快取」的邏輯,不會兩邊各自維護一份、之後改一
// 個忘了改另一個。
func (c *Checker) CheckNow(ctx context.Context, currentVersion, manifestURL string, client *http.Client) CheckResult {
	manifest, err := FetchManifest(ctx, client, manifestURL)
	result := CheckResult{CheckedAt: time.Now()}
	if err != nil {
		result.Err = err
		if c.logger != nil {
			c.logger.Warn("checking for gonasd updates failed", "manifestUrl", manifestURL, "err", err)
		}
	} else {
		newer, _ := IsNewer(currentVersion, manifest.Version)
		result.LatestVersion = manifest.Version
		result.UpdateAvailable = newer
		result.Notes = manifest.Notes
		if newer && c.logger != nil {
			c.logger.Info("a newer gonasd version is available", "current", currentVersion, "latest", manifest.Version)
			if !ManifestSignatureEnforced() {
				// 這個建置沒有內嵌公鑰,更新只靠 SHA256 + HTTPS 傳輸來源可信,
				// 沒有數位簽章驗證。提醒運維:能掌控 manifest 來源的人就能推
				// 任意(以 root 執行的)執行檔。要更強的保證,用內嵌公鑰的建置
				// 並對 manifest 簽章,見 selfupdate.ManifestPublicKeyHex 的說明。
				c.logger.Warn("gonasd self-update is NOT signature-verified (no embedded public key) — trust rests entirely on the HTTPS manifest source; whoever controls it can push an arbitrary root binary")
			}
		}
	}

	c.mu.Lock()
	c.latest = result
	c.mu.Unlock()
	return result
}

// Start 依 checkInterval 週期性檢查,啟動當下也會立刻先檢查一次(不用
// 等第一個週期過去)。currentVersion 是拿來跟 Manifest 裡的版本比較的
// 基準(通常是 internal/version.Version)。
func (c *Checker) Start(ctx context.Context, checkInterval time.Duration, currentVersion string, getManifestURL func() string, client *http.Client) {
	ctx, cancel := context.WithCancel(ctx)
	c.cancel = cancel
	c.done = make(chan struct{})

	check := func() {
		manifestURL := getManifestURL()
		if manifestURL == "" {
			return
		}
		c.CheckNow(ctx, currentVersion, manifestURL, client)
	}

	go func() {
		defer close(c.done)
		safe.Run(c.logger, "update-checker", check)

		ticker := time.NewTicker(checkInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				safe.Run(c.logger, "update-checker", check)
			}
		}
	}()
}

// Stop 讓檢查 goroutine 結束,並等它真的結束才回傳。在還沒呼叫過
// Start 的情況下是安全的 no-op。
func (c *Checker) Stop() {
	if c.cancel == nil {
		return
	}
	c.cancel()
	<-c.done
}

// progressWriter 在每次寫入後回報累計位元組數。
type progressWriter struct {
	w     io.Writer
	done  int64
	total int64
	fn    func(done, total int64)
}

func (p *progressWriter) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	p.done += int64(n)
	p.fn(p.done, p.total)
	return n, err
}
