package filemanager

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// maxSearchResults / maxSearchScanned 幫搜尋加兩道上限：陣列可能有幾百萬
// 個檔案，一次不設限的遞迴搜尋在最壞情況下可能跑很久、佔用大量記憶體
// 組裝結果。maxSearchResults 限制回傳給前端的筆數(找到這麼多通常已經
// 足夠使用者往下縮小關鍵字)，maxSearchScanned 則是「最多看過幾個項目
// 就不繼續往下掃了」的獨立上限——即使關鍵字幾乎沒有命中，也不會讓一次
// 搜尋請求無限期地掃完整個陣列。兩者都超過時，SearchResult.Truncated
// 會是 true，讓 Web UI 可以提示使用者結果可能不完整、建議縮小搜尋範圍。
const (
	maxSearchResults = 500
	maxSearchScanned = 200000
)

// SearchResult 是一次搜尋的完整結果。
type SearchResult struct {
	Entries   []Entry `json:"entries"`
	Truncated bool    `json:"truncated"`
}

// Search 從 relPath 開始遞迴往下找檔名包含 query(不分大小寫)的項目。
// 回收桶資料夾一律跳過，不管使用者從哪一層開始搜尋——回收桶裡的東西
// 語意上已經是「使用者刪掉的東西」，混進一般搜尋結果只會讓人誤以為
// 陣列裡還有這份檔案。
func Search(root, relPath, query string) (SearchResult, error) {
	start, err := resolve(root, relPath)
	if err != nil {
		return SearchResult{}, err
	}
	info, err := os.Stat(start)
	if err != nil {
		if os.IsNotExist(err) {
			return SearchResult{}, ErrNotFound
		}
		return SearchResult{}, fmt.Errorf("searching %q: %w", relPath, err)
	}
	if !info.IsDir() {
		return SearchResult{}, ErrNotADirectory
	}

	query = strings.ToLower(strings.TrimSpace(query))
	result := SearchResult{Entries: []Entry{}}
	if query == "" {
		return result, nil
	}

	scanned := 0
	walkErr := filepath.WalkDir(start, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // 單一項目讀取失敗(權限問題等)不該讓整個搜尋失敗，跳過繼續
		}
		if p != start && d.IsDir() && d.Name() == trashDirName {
			return filepath.SkipDir
		}
		if p == start {
			return nil
		}

		scanned++
		if scanned > maxSearchScanned {
			result.Truncated = true
			return filepath.SkipAll
		}

		if !strings.Contains(strings.ToLower(d.Name()), query) {
			return nil
		}

		fi, err := d.Info()
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return nil
		}
		result.Entries = append(result.Entries, Entry{
			Name:    d.Name(),
			Path:    path.Clean(filepath.ToSlash(rel)),
			IsDir:   d.IsDir(),
			Size:    fi.Size(),
			ModTime: fi.ModTime(),
		})
		if len(result.Entries) >= maxSearchResults {
			result.Truncated = true
			return filepath.SkipAll
		}
		return nil
	})
	if walkErr != nil {
		return SearchResult{}, fmt.Errorf("searching %q: %w", relPath, walkErr)
	}
	return result, nil
}
