package filemanager

import (
	"fmt"
	"os"
	"path/filepath"
	"unicode/utf8"
)

// maxTextFileBytes 是「當成文字檔在瀏覽器裡預覽/編輯」的大小上限。
// 設定檔、腳本、筆記這類 GoNAS 使用者實際上會想直接在瀏覽器裡改的檔案
// 幾乎不可能超過這個大小；超過的話代表這大概是一個二進位檔或大型資料
// 檔，硬塞進一個 <textarea> 對瀏覽器/使用者都不友善(下載/用專門的工具
// 打開才是正確的路)。
const maxTextFileBytes = 2 << 20 // 2 MiB

// ReadTextFile 讀出一個檔案的完整內容當文字使用。只接受看起來是 UTF-8
// 文字、大小在 maxTextFileBytes 以內的檔案——這是「預覽/編輯」用途，
// 不是通用的檔案讀取 API(那是 ResolvePath + os.Open 給下載端點用的)。
func ReadTextFile(root, relPath string) (string, error) {
	target, err := resolve(root, relPath)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(target)
	if err != nil {
		if os.IsNotExist(err) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("reading %q: %w", relPath, err)
	}
	if info.IsDir() {
		return "", ErrIsADirectory
	}
	if info.Size() > maxTextFileBytes {
		return "", ErrFileTooLargeForTextEdit
	}

	data, err := os.ReadFile(target)
	if err != nil {
		return "", fmt.Errorf("reading %q: %w", relPath, err)
	}
	if !utf8.Valid(data) {
		return "", ErrNotValidUTF8Text
	}
	return string(data), nil
}

// WriteTextFile 把 content 寫成 relPath 的完整內容,取代原本的內容
// (檔案不存在就新建)。用「先寫暫存檔、成功後再 rename 蓋過去」的
// 方式做到接近原子性的寫入——半途斷線或磁碟滿了不會讓目標檔案處於
// 內容損毀的中間狀態，跟 internal/state 保存設定檔用的是同一種手法。
func WriteTextFile(root, relPath, content string) error {
	if len(content) > maxTextFileBytes {
		return ErrFileTooLargeForTextEdit
	}
	target, err := resolve(root, relPath)
	if err != nil {
		return err
	}
	if info, err := os.Stat(target); err == nil && info.IsDir() {
		return ErrIsADirectory
	}

	dir := filepath.Dir(target)
	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: parent directory does not exist", ErrNotFound)
		}
		return fmt.Errorf("checking parent directory: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".gonas-edit-*")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // rename 成功之後這行是 no-op

	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return fmt.Errorf("writing temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing temp file: %w", err)
	}
	if err := os.Rename(tmpPath, target); err != nil {
		return fmt.Errorf("replacing %q: %w", relPath, err)
	}
	return nil
}
