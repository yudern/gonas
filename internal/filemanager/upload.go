package filemanager

import (
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// SaveStream 把 r 的內容存成 dirRel 底下名為 filename 的新檔案，回傳存好
// 之後的 Entry。用在檔案上傳端點——HTTP 層負責把 multipart 請求拆成一個
// 一個檔案的 io.Reader,實際「寫進陣列哪個位置」的邏輯留在這個套件裡，
// 跟其他操作共用同一套路徑安全檢查。
//
// 目的地已經有同名檔案時會直接覆蓋——這是大多數檔案管理員「上傳同名
// 檔案 = 更新內容」的預期行為，跟 Move/Copy 刻意拒絕覆蓋(避免搬移/
// 複製誤觸)是不同的操作類型，語意上不用強求一致。
func SaveStream(root, dirRel, filename string, r io.Reader) (Entry, error) {
	if filename == "" || strings.ContainsAny(filename, "/\\") || filename == "." || filename == ".." {
		return Entry{}, fmt.Errorf("%w: %q", ErrInvalidName, filename)
	}
	dir, err := resolve(root, dirRel)
	if err != nil {
		return Entry{}, err
	}
	info, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return Entry{}, ErrNotFound
		}
		return Entry{}, fmt.Errorf("checking destination directory: %w", err)
	}
	if !info.IsDir() {
		return Entry{}, ErrNotADirectory
	}

	target := filepath.Join(dir, filename)
	// 先寫到暫存檔,成功後再 rename 蓋過去,避免上傳到一半斷線在陣列裡
	// 留下一個看起來存在、內容卻不完整的檔案。
	tmp, err := os.CreateTemp(dir, ".gonas-upload-*")
	if err != nil {
		return Entry{}, fmt.Errorf("creating temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // rename 成功之後這行是 no-op

	if _, err := io.Copy(tmp, r); err != nil {
		tmp.Close()
		return Entry{}, fmt.Errorf("writing upload: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return Entry{}, fmt.Errorf("closing upload: %w", err)
	}
	if err := os.Rename(tmpPath, target); err != nil {
		return Entry{}, fmt.Errorf("finalizing upload: %w", err)
	}

	fi, err := os.Stat(target)
	if err != nil {
		return Entry{}, fmt.Errorf("reading uploaded file info: %w", err)
	}
	return Entry{
		Name:    filename,
		Path:    path.Join(dirRel, filename),
		IsDir:   false,
		Size:    fi.Size(),
		ModTime: fi.ModTime(),
	}, nil
}
