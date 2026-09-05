// Package filemanager 讓 Web UI 可以直接瀏覽、上傳、下載、搬移使用者存在
// GoNAS 儲存陣列裡的檔案——這是除了 SMB/NFS 之外，唯一不需要另外安裝
// 用戶端軟體、瀏覽器打開就能用的檔案存取方式。對「這台 NAS 好不好用」
// 這個目標來說，這個套件跟儲存陣列本身一樣重要：陣列再穩，使用者也得
// 有一個簡單的地方可以直接看到、整理自己的檔案。
//
// 所有操作都相對於一個「根目錄」(呼叫端傳進來，通常是
// internal/storage 回報的陣列掛載點)，函式簽章一律接受「相對路徑」
// (用 "/" 分隔、不含前導斜線，"" 代表根目錄本身)。這個套件負責把相對
// 路徑安全地轉成根目錄底下的絕對路徑，並且拒絕任何試圖跳出根目錄的
// 路徑——這是這個套件存在的最主要的資安考量，見 resolve 的說明。
package filemanager

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"time"
)

// trashDirName 是回收桶在根目錄底下的資料夾名稱。用一個以 "." 開頭、
// 刻意跟 GoNAS 自己命名慣例綁在一起的名字，降低跟使用者自己的檔案/
// 資料夾撞名的機率；List 會把這個名字從一般瀏覽結果裡濾掉，見 List 的
// 說明。
const trashDirName = ".gonas-trash"

// 套件對外的 sentinel 錯誤。internal/api 的 handler 層會把這些對應到
// 合適的 HTTP 狀態碼(見 internal/api/files_handlers.go 的
// writeFileManagerError)。
var (
	ErrNoRoot                  = errors.New("filemanager: no root directory configured")
	ErrPathEscapesRoot         = errors.New("filemanager: path escapes the root directory")
	ErrNotFound                = errors.New("filemanager: no such file or directory")
	ErrAlreadyExists           = errors.New("filemanager: a file or directory with that name already exists")
	ErrNotADirectory           = errors.New("filemanager: not a directory")
	ErrIsADirectory            = errors.New("filemanager: is a directory, not a file")
	ErrInvalidName             = errors.New("filemanager: invalid file name")
	ErrFileTooLargeForTextEdit = errors.New("filemanager: file is too large to preview or edit as text")
	ErrNotValidUTF8Text        = errors.New("filemanager: file does not look like a UTF-8 text file")
)

// Entry 是一筆目錄列表項目，也是搜尋結果的元素型別。
type Entry struct {
	Name    string    `json:"name"`
	Path    string    `json:"path"` // 相對路徑，用 "/" 分隔，不含前導斜線
	IsDir   bool      `json:"isDir"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"modTime"`
}

// resolve 把一個呼叫端傳進來的相對路徑，安全地轉成 root 底下的絕對路徑。
//
// 防禦分兩層：
//  1. 純路徑字串層級：用 path.Clean 處理掉 "."/".." 這些片段之後，
//     確認結果真的落在 root 底下(不是單純檢查字串裡有沒有 ".."——
//     Clean 之後比對前綴才是正確作法，否則像 "a/../../b" 這種輸入
//     可能被字串比對誤判)。
//  2. 檔案系統層級(symlink 逃逸防禦):防禦使用者(或不小心透過
//     Samba/NFS 寫進陣列的內容)在陣列裡放一個指向陣列外部的符號連結，
//     透過一個看起來完全合法的相對路徑逃出根目錄。做法是把 root 跟
//     目標路徑「目前已存在的那一段祖先目錄」都解到真實路徑再比一次
//     前綴——目標本身可能還不存在(例如要新建的檔案),所以從目標開始
//     往上找第一個真的存在的祖先。
//
// 這兩層都只防禦「透過相對路徑逃出 root」，不會、也不需要阻止 root
// 底下的內容裡有指向 root 內部其他地方的 symlink——那只是使用者自己
// 在陣列裡建的捷徑，不是安全問題。
func resolve(root, relPath string) (string, error) {
	if root == "" {
		return "", ErrNoRoot
	}

	cleaned := path.Clean("/" + relPath)
	abs := filepath.Join(root, filepath.FromSlash(cleaned))

	if err := checkWithinRoot(root, abs); err != nil {
		return "", err
	}
	if err := checkNoSymlinkEscape(root, abs); err != nil {
		return "", err
	}
	return abs, nil
}

func checkWithinRoot(root, abs string) error {
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == ".." || hasDotDotPrefix(rel) {
		return fmt.Errorf("%w: resolved path escapes root", ErrPathEscapesRoot)
	}
	return nil
}

func hasDotDotPrefix(rel string) bool {
	return len(rel) >= 3 && rel[0] == '.' && rel[1] == '.' && os.IsPathSeparator(rel[2])
}

func checkNoSymlinkEscape(root, abs string) error {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("resolving root directory: %w", err)
	}

	// 從目標路徑開始往上找第一個真的存在的祖先目錄——新建檔案/資料夾
	// 時，目標本身理所當然還不存在。
	existing := abs
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			// 已經到檔案系統根目錄還是找不到存在的祖先，理論上不會發生
			// (root 自己至少要存在),保守起見當成逃逸處理。
			return fmt.Errorf("%w: no existing ancestor directory found", ErrPathEscapesRoot)
		}
		existing = parent
	}

	realExisting, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return fmt.Errorf("resolving path: %w", err)
	}
	if realExisting == realRoot {
		return nil
	}
	rel, err := filepath.Rel(realRoot, realExisting)
	if err != nil || rel == ".." || hasDotDotPrefix(rel) {
		return fmt.Errorf("%w: a symlink leads outside the root directory", ErrPathEscapesRoot)
	}
	return nil
}

// ResolvePath 把一個相對路徑解析成 root 底下的絕對路徑(套用跟這個套件
// 其他函式一樣的逃逸防禦),供呼叫端(HTTP handler)在需要直接用
// os.Open/http.ServeContent 這類標準函式庫工具處理檔案內容(例如下載、
// 串流)時使用，不用重新自己刻一份路徑檢查邏輯。
func ResolvePath(root, relPath string) (string, error) {
	return resolve(root, relPath)
}

// List 列出一個目錄底下的項目。刻意濾掉回收桶資料夾，避免使用者在
// 一般瀏覽畫面看到一個自己沒建過、名字又有點奇怪的資料夾而困惑——
// 回收桶有自己專門的 API(見 trash.go)。
func List(root, relPath string) ([]Entry, error) {
	dir, err := resolve(root, relPath)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("listing %q: %w", relPath, err)
	}
	if !info.IsDir() {
		return nil, ErrNotADirectory
	}

	items, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("listing %q: %w", relPath, err)
	}

	entries := make([]Entry, 0, len(items))
	for _, de := range items {
		if de.Name() == trashDirName {
			continue
		}
		fi, err := de.Info()
		if err != nil {
			// 列出目錄跟 Stat 個別項目之間檔案被刪掉是正常的競態情況，
			// 跳過這一筆而不是讓整個請求失敗。
			continue
		}
		entries = append(entries, Entry{
			Name:    de.Name(),
			Path:    path.Join(relPath, de.Name()),
			IsDir:   de.IsDir(),
			Size:    fi.Size(),
			ModTime: fi.ModTime(),
		})
	}
	return entries, nil
}
