package filemanager

import (
	"archive/zip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// WriteZip 把 relPath 底下的整個目錄樹壓縮成一個 zip 檔，直接寫進 w
// (通常是 HTTP 回應本身)——邊走訪邊寫進 zip.Writer，不會先在記憶體或
// 磁碟組出完整的 zip 再一次送出，資料夾多大都不會讓伺服器記憶體用量
// 跟著炸開。
//
// zip 裡的路徑刻意保留 relPath 自己這一層資料夾名稱當最上層(而不是
// 直接從它的內容開始),這樣解壓縮出來會是一個資料夾，行為跟大多數
// 檔案總管的「壓縮下載」一致，使用者不會不小心把好幾個檔案直接解到
// 目前所在的目錄裡跟其他東西混在一起。回收桶資料夾、跟資料夾樹裡的
// symlink 一律跳過,理由跟 Copy 的說明一樣。
func WriteZip(w io.Writer, root, relPath string) error {
	dir, err := resolve(root, relPath)
	if err != nil {
		return err
	}
	info, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return fmt.Errorf("reading %q: %w", relPath, err)
	}
	if !info.IsDir() {
		return ErrNotADirectory
	}

	zw := zip.NewWriter(w)
	defer zw.Close()

	parent := filepath.Dir(dir)

	return filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // 單一項目讀取失敗不該讓整份下載中斷，跳過繼續
		}
		if d.IsDir() && d.Name() == trashDirName {
			return filepath.SkipDir
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}

		rel, err := filepath.Rel(parent, p)
		if err != nil {
			return err
		}
		zipPath := filepath.ToSlash(rel)

		if d.IsDir() {
			_, err := zw.Create(zipPath + "/")
			return err
		}

		fi, err := d.Info()
		if err != nil {
			return nil
		}
		header, err := zip.FileInfoHeader(fi)
		if err != nil {
			return nil
		}
		header.Name = zipPath
		header.Method = zip.Deflate

		entry, err := zw.CreateHeader(header)
		if err != nil {
			return err
		}
		f, err := os.Open(p)
		if err != nil {
			return nil // 單一檔案打不開(權限等)跳過，不中斷整份下載
		}
		defer f.Close()
		_, err = io.Copy(entry, f)
		return err
	})
}
