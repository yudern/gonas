package storage

import (
	"bytes"
	"context"
	"fmt"
	"text/template"
)

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

// GenerateSnapraidConfig 依照 pool 設定產生 snapraid.conf 的內容。
// 回傳字串而不是直接寫檔,方便測試直接比對內容,也讓呼叫端決定要不要
// 先寫到暫存檔再原子性地換過去(避免寫到一半程序被砍掉,留下半份設定檔）。
func GenerateSnapraidConfig(cfg PoolConfig) (string, error) {
	if err := cfg.Validate(); err != nil {
		return "", fmt.Errorf("refusing to generate config for invalid pool: %w", err)
	}
	var buf bytes.Buffer
	if err := snapraidTmpl.Execute(&buf, cfg); err != nil {
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

// RunSnapraid 對指定設定檔執行一個 SnapRAID 子指令,回傳 stdout 供上層記錄。
func RunSnapraid(ctx context.Context, r Runner, configPath string, action SnapraidAction) ([]byte, error) {
	out, err := r.Run(ctx, "snapraid", "-c", configPath, string(action))
	if err != nil {
		return out, fmt.Errorf("snapraid %s (config=%s): %w", action, configPath, err)
	}
	return out, nil
}
