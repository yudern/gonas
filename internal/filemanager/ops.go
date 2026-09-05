package filemanager

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Mkdir 在 relPath 建立一個新的空目錄。刻意只建立最後一層(不像
// os.MkdirAll 那樣連中間目錄一起建)——Web UI 的「新增資料夾」表單只
// 應該在使用者目前瀏覽的目錄底下建一層，中間路徑不存在通常代表呼叫端
// 邏輯有誤，直接回報錯誤比默默建一整串目錄更不容易讓使用者搞不清楚
// 陣列裡多出了哪些資料夾。
func Mkdir(root, relPath string) error {
	target, err := resolve(root, relPath)
	if err != nil {
		return err
	}
	if base := filepath.Base(target); base == "." || base == string(filepath.Separator) {
		return fmt.Errorf("%w: cannot create the root directory itself", ErrInvalidName)
	}
	if _, err := os.Lstat(target); err == nil {
		return ErrAlreadyExists
	}
	if err := os.Mkdir(target, 0o755); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: parent directory does not exist", ErrNotFound)
		}
		return fmt.Errorf("creating directory %q: %w", relPath, err)
	}
	return nil
}

// Move 把 srcRel 搬到 dstRel(涵蓋「搬到別的資料夾」跟「原地改名」兩種
// 情境，語意上是同一件事——目的地路徑不同而已)。目的地已經存在時拒絕
// 執行,避免不小心蓋掉別的檔案；需要覆蓋的話，呼叫端應該先讓使用者刪掉
// 目的地或明確選擇覆蓋(GoNAS 目前的 Web UI 沒有提供這個選項，刻意保守)。
func Move(root, srcRel, dstRel string) error {
	src, err := resolve(root, srcRel)
	if err != nil {
		return err
	}
	dst, err := resolve(root, dstRel)
	if err != nil {
		return err
	}
	if src == dst {
		return nil
	}
	if _, err := os.Lstat(src); err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return fmt.Errorf("reading %q: %w", srcRel, err)
	}
	if _, err := os.Lstat(dst); err == nil {
		return ErrAlreadyExists
	}

	if err := os.Rename(src, dst); err != nil {
		// mergerFS 這類聯合檔案系統，把檔案從一個底層磁碟的分支「搬」到
		// 另一個分支時，有可能在系統呼叫層級回傳 EXDEV(不同檔案系統之間
		// 不能直接 rename)。退化成「複製後刪除原檔」，使用者感覺不出
		// 差異，只是搬移大檔案時會慢一點、且過程中會短暫同時佔用兩份空間。
		if errors.Is(err, syscall.EXDEV) {
			return moveCrossDevice(src, dst)
		}
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return fmt.Errorf("moving %q to %q: %w", srcRel, dstRel, err)
	}
	return nil
}

// Copy 把 srcRel 複製一份到 dstRel，來源不受影響。目錄會遞迴複製；
// 複製過程中遇到的 symlink 會被跳過而不是被追蹤複製——追蹤 symlink
// 可能複製到 root 以外的內容，見套件說明對「陣列裡有指到外部的
// symlink」這個情境的處理原則。
func Copy(root, srcRel, dstRel string) error {
	src, err := resolve(root, srcRel)
	if err != nil {
		return err
	}
	dst, err := resolve(root, dstRel)
	if err != nil {
		return err
	}
	info, err := os.Lstat(src)
	if err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return fmt.Errorf("reading %q: %w", srcRel, err)
	}
	if _, err := os.Lstat(dst); err == nil {
		return ErrAlreadyExists
	}
	// 目的地所在的資料夾必須已經存在——跟 Move 的行為一致(Move 底層靠
	// os.Rename 在目的地父目錄不存在時自然回傳 ENOENT，這裡用同樣的
	// 語意明確檢查一次)。少了這個檢查，複製單一檔案時 copyFile 會在
	// 一個不存在的目錄底下呼叫 os.CreateTemp 失敗，錯誤會夾帶伺服器上
	// 的真實絕對路徑一路冒到 HTTP 回應、且被當成非預期錯誤回 500，而不是
	// 「目的資料夾不存在」這種好懂、乾淨的 404。
	if _, err := os.Stat(filepath.Dir(dst)); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: destination folder does not exist", ErrNotFound)
		}
		return fmt.Errorf("checking destination folder for %q: %w", dstRel, err)
	}

	if info.IsDir() {
		return copyDir(src, dst)
	}
	return copyFile(src, dst, info.Mode())
}

// Delete 刪除 relPath。permanent=false(預設,對應 Web UI 一般的「刪除」
// 按鈕)時搬進回收桶,可以之後用 trash.go 的 Restore 復原；permanent=true
// 才是真的、立即、不能復原的刪除,見 trash.go 的整體說明。
func Delete(root, relPath string, permanent bool) error {
	target, err := resolve(root, relPath)
	if err != nil {
		return err
	}
	info, err := os.Lstat(target)
	if err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return fmt.Errorf("reading %q: %w", relPath, err)
	}

	if !permanent {
		return moveToTrash(root, relPath, target, info)
	}
	if info.IsDir() {
		return os.RemoveAll(target)
	}
	return os.Remove(target)
}

func moveCrossDevice(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return fmt.Errorf("reading source: %w", err)
	}
	if info.IsDir() {
		if err := copyDir(src, dst); err != nil {
			return err
		}
	} else {
		if err := copyFile(src, dst, info.Mode()); err != nil {
			return err
		}
	}
	return os.RemoveAll(src)
}

func copyFile(src, dst string, mode fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("opening source file: %w", err)
	}
	defer in.Close()

	// 先寫到目的地目錄底下的暫存檔再 rename 過去，避免複製到一半失敗
	// (磁碟滿了、程序被砍)在目的地留下一個內容不完整的檔案。
	dir := filepath.Dir(dst)
	tmp, err := os.CreateTemp(dir, ".gonas-copy-*")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // 成功 rename 之後這行是 no-op(檔案已經不在原地了)

	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		return fmt.Errorf("copying file contents: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing temp file: %w", err)
	}
	if err := os.Chmod(tmpPath, mode); err != nil {
		return fmt.Errorf("setting permissions: %w", err)
	}
	if err := os.Rename(tmpPath, dst); err != nil {
		return fmt.Errorf("finalizing copy: %w", err)
	}
	return nil
}

// copyDir 遞迴複製一個目錄。symlink 一律跳過(見 Copy 的說明),既有的
// 檔案類型(一般檔案/目錄)才複製。
func copyDir(src, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return fmt.Errorf("creating destination directory: %w", err)
	}
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == src {
			return nil
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)

		if d.Type()&fs.ModeSymlink != 0 {
			return nil // 跳過 symlink，不追蹤、不複製
		}
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return copyFile(p, target, info.Mode())
	})
}

// sanitizeTrashName 把原始檔名裡可能干擾檔案系統路徑的字元換掉，只用在
// 組出回收桶裡的暫存檔名，不影響 Restore 時寫回去的原始檔名(那個存在
// meta 檔案裡，是完整、未經清理的原始檔名)。
func sanitizeTrashName(name string) string {
	return strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' {
			return '_'
		}
		return r
	}, name)
}
