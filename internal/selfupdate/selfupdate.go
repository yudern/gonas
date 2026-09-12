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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
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

// FetchManifest 用 client 對 manifestURL 發一個 GET 請求並解析回應的
// JSON。client 是呼叫端注入的(而不是用 http.DefaultClient),方便測試
// 控制逾時/傳輸行為,也讓正式呼叫端能套用專案一致的逾時設定。
func FetchManifest(ctx context.Context, client *http.Client, manifestURL string) (Manifest, error) {
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
	written, err := io.Copy(io.MultiWriter(tmp, hasher), limited)
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
