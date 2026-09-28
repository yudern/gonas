package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"text/template"
)

// ErrSnapraidNotInstalled 是固定英文錯誤(前端 errorMap 翻譯):同位校驗需要
// snapraid 這個外部程式,但它還沒安裝(見 ErrMergerfsNotInstalled 的說明,同理)。
var ErrSnapraidNotInstalled = errors.New("snapraid is not installed — parity protection needs it; install it from the System Doctor page (or it comes preinstalled on the offline appliance image)")

// snapraidConfTemplate 產生 SnapRAID 設定檔內容。格式規格見
// https://www.snapraid.it/manual —— 這裡只用得到最核心的 parity/content/data
// 三種指令,exclude 清單先給幾個常見的雜訊檔案(縮圖快取、Docker 疊層等),
// 之後 Web UI 會讓使用者自訂。
const snapraidConfTemplate = `# 由 GoNAS 自動產生,請勿手動修改 —— 修改請透過 Web UI 或 API。
# pool: {{.Name}}

{{range $i, $p := .ParityDisks}}parity {{$p}}/snapraid.parity
{{end}}
{{range $i, $c := .ContentFiles}}content {{$c}}/snapraid.content
{{end}}
{{range $i, $d := .DataDisks}}data d{{inc $i}} {{$d}}
{{end}}
# 排除常見的雜訊 / 高變動檔案,避免拖慢 sync 也避免無意義地佔用同位空間
exclude *.tmp
exclude *.temp
exclude /lost+found/
exclude /.Trash-*/
exclude /docker/overlay2/
`

var snapraidTmpl = template.Must(template.New("snapraid.conf").Funcs(template.FuncMap{
	"inc": func(i int) int { return i + 1 },
}).Parse(snapraidConfTemplate))

// naturalLess 是「人類直覺」的字串比較:把數字段當數字比,所以
// /mnt/disk2 排在 /mnt/disk10 前面(純字典序會反過來)。用來對資料碟掛載點
// 做穩定且好懂的排序。
func naturalLess(a, b string) bool {
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		ca, cb := a[i], b[j]
		da, db := ca >= '0' && ca <= '9', cb >= '0' && cb <= '9'
		if da && db {
			// 各自吃掉一整段連續數字,去掉前導 0 後先比長度、再比字典序。
			si, sj := i, j
			for i < len(a) && a[i] >= '0' && a[i] <= '9' {
				i++
			}
			for j < len(b) && b[j] >= '0' && b[j] <= '9' {
				j++
			}
			na := strings.TrimLeft(a[si:i], "0")
			nb := strings.TrimLeft(b[sj:j], "0")
			if len(na) != len(nb) {
				return len(na) < len(nb)
			}
			if na != nb {
				return na < nb
			}
			continue
		}
		if ca != cb {
			return ca < cb
		}
		i++
		j++
	}
	return len(a)-i < len(b)-j
}

// GenerateSnapraidConfig 依照 pool 設定產生 snapraid.conf 的內容。
// 回傳字串而不是直接寫檔,方便測試直接比對內容,也讓呼叫端決定要不要
// 先寫到暫存檔再原子性地換過去(避免寫到一半程序被砍掉,留下半份設定檔）。
//
// 第六十輪(使用者實機定位到的根因):snapraid 用 data 行的 dN 名稱去記住
// 「哪顆碟是哪顆」,並把每個 dN 綁到該掛載點當下檔案系統的 UUID。原本這裡
// 直接照 cfg.DataDisks「傳進來的順序」編 d1/d2/d3——而那個順序來自前端,前端
// 又來自 lsblk 的裝置節點順序(/dev/sdb、/dev/sdc…),這個順序會隨開機/插拔
// 變動。於是使用者「用嚮導建好池(當下 d1=disk1、d2=disk2)之後,回到儲存頁
// 再點一次『儲存設定』」時,若裝置節點順序變了,dN↔掛載點的對應就跟著顛倒,
// snapraid 立刻報「d1、d2 的 UUID 互換了」而拒絕 sync——盤沒事、資料沒事,純粹
// 是 dN 編號漂移。
//
// 修法:dN 的指派改成「與傳入順序無關」——先照掛載點路徑做穩定排序(自然排序,
// /mnt/disk2 在 /mnt/disk10 前)再編號。這樣同一組資料碟不管前端送來的順序如何,
// 永遠得到同一份 data 行、同一組 dN↔掛載點對應,再也不會因為重存設定/重開機
// 而漂移。(掛載點本身由 GoNAS 準備磁碟時以 UUID 寫進 fstab,是穩定的,所以
// 依掛載點排序即等於依實體碟穩定排序。)
func GenerateSnapraidConfig(cfg PoolConfig) (string, error) {
	if err := cfg.Validate(); err != nil {
		return "", fmt.Errorf("refusing to generate config for invalid pool: %w", err)
	}
	// 對資料碟做穩定排序後再編 dN——用副本,不動呼叫端傳進來的 slice。
	stable := cfg
	stable.DataDisks = append([]string(nil), cfg.DataDisks...)
	sort.SliceStable(stable.DataDisks, func(i, j int) bool {
		return naturalLess(stable.DataDisks[i], stable.DataDisks[j])
	})
	var buf bytes.Buffer
	if err := snapraidTmpl.Execute(&buf, stable); err != nil {
		return "", fmt.Errorf("rendering snapraid.conf: %w", err)
	}
	return buf.String(), nil
}

// SnapraidAction 是 SnapRAID 支援的幾個核心子指令,故意只列出 Phase 1
// 會用到的三個,而不是整份 SnapRAID CLI(fix/status/smart... 之後陸續補)。
type SnapraidAction string

const (
	// SnapraidDiff 只比對「哪些檔案變了」,不寫入同位資料,拿來檢查 sync
	// 前的變動量,避免在一次 sync 裡誤把大量意外刪除當成正常變動同步掉。
	SnapraidDiff SnapraidAction = "diff"
	// SnapraidSync 把目前資料狀態寫進同位碟,是「正式產生保護」的動作。
	SnapraidSync SnapraidAction = "sync"
	// SnapraidScrub 重新讀取一部分資料並驗證同位是否吻合,用來及早抓到
	// 靜默資料損毀（bit rot),排程週期性執行。
	SnapraidScrub SnapraidAction = "scrub"
)

// looksLikeUUIDChanged 判斷 snapraid 的失敗是不是「太多磁碟的 UUID 變了」
// 這種 —— snapraid 會把每顆資料/同位碟的檔案系統 UUID 記在 content 檔裡,
// 下次 sync 前比對,發現對不上就整個擋下來(輸出類似
// "UUID change for disk 'd1' ... Too many disks have changed UUIDs"),
// 這是它防止「使用者把兩顆碟接錯位置、結果把 A 碟的同位算到 B 碟上」的保護。
//
// 但在 GoNAS 這種 appliance 情境裡,這個保護幾乎都是「誤報」:snapraid.conf
// 是每次 sync 前由 GoNAS 依當前 pool 設定(掛載點)重新產生的,掛載點對應到
// 哪顆實體碟由 fstab 的 UUID 綁定,所以「d1/d2 的 UUID 互換」通常只是磁碟被
// 重新掛載/換了 /dev 裝置節點/在全新 appliance 上第一次 sync,資料本身沒動。
// 這種情況正確的做法就是用 --force-uuid 讓 snapraid 接受新的 UUID 對應。
func looksLikeUUIDChanged(out []byte, err error) bool {
	s := strings.ToLower(string(out))
	if err != nil {
		s += " " + strings.ToLower(err.Error())
	}
	if !strings.Contains(s, "uuid") {
		return false
	}
	return strings.Contains(s, "too many") ||
		strings.Contains(s, "changed") ||
		strings.Contains(s, "change for")
}

// RunSnapraidSync 執行一次 sync;若被 snapraid 以「太多磁碟 UUID 變了」擋下
// (looksLikeUUIDChanged),自動加 --force-uuid 重試一次。回傳最終輸出、是否
// 用了 --force-uuid(讓上層記 log / 回報給使用者),以及最終錯誤。
//
// 第六十輪(使用者實機):使用者按「立即同步校驗」後失敗,截圖顯示正是
// snapraid 擋在 UUID 變更檢查——換位置/重掛之後 sync 就再也做不起來,而 UI
// 上完全沒有「強制」的出口,等於同位保護永遠卡住。這裡在 daemon 端自動處理:
// 偵測到就用 --force-uuid 重試(對 GoNAS 管理的 pool 而言是安全且正確的動作,
// 理由見 looksLikeUUIDChanged),使用者不必自己 SSH 進機器下指令。
func RunSnapraidSync(ctx context.Context, r Runner, configPath string) (out []byte, usedForceUUID bool, err error) {
	out, err = r.Run(ctx, "snapraid", "-c", configPath, "sync")
	if err == nil {
		return out, false, nil
	}
	if looksLikeMissingBinary(err) {
		return out, false, ErrSnapraidNotInstalled
	}
	if looksLikeUUIDChanged(out, err) {
		out2, err2 := r.Run(ctx, "snapraid", "-c", configPath, "--force-uuid", "sync")
		if err2 == nil {
			return out2, true, nil
		}
		if looksLikeMissingBinary(err2) {
			return out2, true, ErrSnapraidNotInstalled
		}
		return out2, true, fmt.Errorf("snapraid sync --force-uuid (config=%s): %w", configPath, err2)
	}
	return out, false, fmt.Errorf("snapraid sync (config=%s): %w", configPath, err)
}

// RunSnapraid 對指定設定檔執行一個 SnapRAID 子指令,回傳 stdout 供上層記錄。
//
// 對 diff 動作的特別處理:實測真正的 snapraid 二進位檔,「diff 有找到
// 差異」的結果會用 exit code 2 表示,而不是 0(詳見
// docs/REAL_HARDWARE_TESTING.md 的 storage 章節)。這是 SnapRAID 自己的正
// 常慣例(exit 0 = 無差異、exit 2 = 有差異、其他 = 真的出錯),不是失敗,
// 所以這裡把 exit code 2 從錯誤裡挑出來,對呼叫端回傳 nil error;sync /
// scrub 則沒有這種語意,任何非零結束碼都仍視為真正的錯誤。
func RunSnapraid(ctx context.Context, r Runner, configPath string, action SnapraidAction) ([]byte, error) {
	out, err := r.Run(ctx, "snapraid", "-c", configPath, string(action))
	if err != nil {
		if action == SnapraidDiff {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) && exitErr.ExitCode() == 2 {
				return out, nil
			}
		}
		if looksLikeMissingBinary(err) {
			// 第五十五輪:snapraid 沒裝時,回看得懂的固定英文句(前端 errorMap
			// 翻譯),而不是 exec 的原始「executable file not found」。
			return out, ErrSnapraidNotInstalled
		}
		return out, fmt.Errorf("snapraid %s (config=%s): %w", action, configPath, err)
	}
	return out, nil
}
