package api

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// 第六十一輪(使用者實機:Doctor 點「離線安裝 mergerFS」仍然報「找不到套件、
// 可能沒有聯網」——明明 ISO 裡有離線包)。沙盒實測重現、定位到的真正根因:
//
//	late-command.sh 3.6 把離線 flat repo 放在 /var/lib/gonas/debs,而
//	install.sh 會把 /var/lib/gonas(GoNAS 私有資料目錄:state.json、密碼雜湊、
//	TLS 私鑰)設成 0750 root:root。apt 讀 file:// 來源時會降權成 `_apt` 使用者,
//	_apt 進不了 0750 的上層目錄 → 「Failed to fetch … Permission denied」→
//	Packages 索引讀不到 → 「Unable to locate package」。對照組:同樣的佈局把
//	上層改成 0755 就裝得起來。
//
// 不能把 /var/lib/gonas 放寬成 0755(那是私密資料目錄),所以把離線倉庫搬到
// 一個獨立、公開可讀的位置 offlineRepoDir,並在 gonasd 啟動時與每次 Doctor
// 安裝前自動把「舊位置」的離線包遷過去、修好權限、重寫 apt 來源——已經裝好的
// 機器只要換上新的 gonasd 就自癒,不需要重灌。

// offlineRepoDir 是離線 flat repo 的新位置(0755,_apt 可讀)。
var offlineRepoDir = "/var/lib/gonas-offline-debs"

// legacyOfflineRepoDir 是第五十八~六十輪 late-command.sh 放離線包的舊位置
// (在 0750 的 /var/lib/gonas 底下,_apt 讀不到)。
var legacyOfflineRepoDir = "/var/lib/gonas/debs"

// offlineRepoListLine 回傳 apt 來源清單應有的內容。
func offlineRepoListLine() string {
	return fmt.Sprintf("deb [trusted=yes] file://%s ./\n", offlineRepoDir)
}

// EnsureOfflineRepo 給 cmd/gonasd 啟動時呼叫(best-effort,只記 log)。
func EnsureOfflineRepo(logger *slog.Logger) {
	if err := ensureOfflineRepo(logger); err != nil {
		logger.Warn("could not fully prepare the bundled offline apt repo", "dir", offlineRepoDir, "err", err)
	}
}

// ensureOfflineRepo 確保離線倉庫在 _apt 讀得到的位置、權限正確、apt 來源指向
// 它。冪等:重複呼叫不會有副作用。機器上根本沒有離線包時什麼都不做。
func ensureOfflineRepo(logger *slog.Logger) error {
	newIndex := filepath.Join(offlineRepoDir, "Packages")
	legacyIndex := filepath.Join(legacyOfflineRepoDir, "Packages")

	// 1. 舊位置 → 新位置(只在新位置還沒有倉庫時搬)。
	if fileExists(legacyIndex) && !fileExists(newIndex) {
		if err := os.MkdirAll(filepath.Dir(offlineRepoDir), 0o755); err != nil {
			return fmt.Errorf("creating parent of %s: %w", offlineRepoDir, err)
		}
		// 新位置可能是個殘留的空目錄(或半套),先清掉才能 rename。
		_ = os.RemoveAll(offlineRepoDir)
		if err := os.Rename(legacyOfflineRepoDir, offlineRepoDir); err != nil {
			// 跨檔案系統等情況 rename 不行 → 逐檔複製,成功後再刪舊的。
			if cerr := copyDirFlat(legacyOfflineRepoDir, offlineRepoDir); cerr != nil {
				return fmt.Errorf("moving offline repo %s -> %s: rename: %v; copy: %w", legacyOfflineRepoDir, offlineRepoDir, err, cerr)
			}
			_ = os.RemoveAll(legacyOfflineRepoDir)
		}
		logger.Info("moved bundled offline apt repo out of the private data dir so apt (_apt user) can read it",
			"from", legacyOfflineRepoDir, "to", offlineRepoDir)
	}

	if !fileExists(newIndex) {
		return nil // 這台機器沒有內建離線包,沒事可做。
	}

	// 2. 權限:目錄 0755、檔案 0644——apt 降權成 _apt 後才讀得到。
	if err := filepath.WalkDir(offlineRepoDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		mode := fs.FileMode(0o644)
		if d.IsDir() {
			mode = 0o755
		}
		return os.Chmod(p, mode)
	}); err != nil {
		return fmt.Errorf("fixing permissions under %s: %w", offlineRepoDir, err)
	}
	if bad := untraversableAncestor(offlineRepoDir); bad != "" {
		logger.Warn("a parent directory of the offline apt repo is not world-traversable; apt (_apt) may still fail to read it",
			"dir", bad)
	}

	// 3. apt 來源清單指向新位置(內容不同才重寫)。
	want := offlineRepoListLine()
	if cur, err := os.ReadFile(offlineSourceList); err != nil || string(cur) != want {
		if err := os.MkdirAll(filepath.Dir(offlineSourceList), 0o755); err != nil {
			return fmt.Errorf("creating %s: %w", filepath.Dir(offlineSourceList), err)
		}
		if err := os.WriteFile(offlineSourceList, []byte(want), 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", offlineSourceList, err)
		}
		logger.Info("pointed the local offline apt source at the bundled repo", "list", offlineSourceList, "repo", offlineRepoDir)
	}
	return nil
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

// untraversableAncestor 回傳第一個「其他使用者沒有 x 權限」的上層目錄(空字串
// 代表都 OK)。只用來記警告,不去改系統目錄。
func untraversableAncestor(dir string) string {
	p := filepath.Clean(dir)
	for {
		parent := filepath.Dir(p)
		if parent == p {
			return ""
		}
		if st, err := os.Stat(parent); err == nil && st.Mode().Perm()&0o001 == 0 {
			return parent
		}
		p = parent
	}
}

// copyDirFlat 把 src 底下的一般檔案複製到 dst(flat repo 只有一層)。
func copyDirFlat(src, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		if err := copyFile(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// aptErrorDetail 從 apt 的輸出裡挑出真正有用的行(E:/W: 錯誤、相依性問題),
// 最多回傳最後 maxLines 行,給前端直接顯示「apt 的真實報錯」,不再被一句
// 籠統的「可能沒有聯網」蓋掉。
func aptErrorDetail(out []byte, err error, maxLines int) string {
	var picked []string
	text := string(out)
	if err != nil {
		text += "\n" + err.Error()
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(text, "\n") {
		l := strings.TrimSpace(line)
		if l == "" || seen[l] {
			continue
		}
		low := strings.ToLower(l)
		if strings.HasPrefix(l, "E:") || strings.HasPrefix(l, "W:") || strings.HasPrefix(l, "Err:") ||
			strings.Contains(low, "depends:") || strings.Contains(low, "unmet dependencies") ||
			strings.Contains(low, "permission denied") || strings.Contains(low, "exit status") {
			seen[l] = true
			picked = append(picked, l)
		}
	}
	if len(picked) == 0 {
		// 沒抓到典型錯誤行 → 退而求其次,給最後幾行原始輸出。
		for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
			if l := strings.TrimSpace(line); l != "" {
				picked = append(picked, l)
			}
		}
	}
	if len(picked) > maxLines {
		picked = picked[len(picked)-maxLines:]
	}
	d := strings.Join(picked, "\n")
	const maxLen = 4000
	if len(d) > maxLen {
		d = d[len(d)-maxLen:]
	}
	return d
}

// errOfflineInstallFailed:機器上有內建離線倉庫,但從它安裝失敗(而且連不到
// 網路鏡像可以退回)。前端 errorMap 翻譯;真實原因放在回應的 detail 欄位。
var errOfflineInstallFailed = errors.New("installing from the bundled offline package repo failed — see the apt output below for the exact reason")
