package filemanager

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// 回收桶存在的理由很直接：檔案管理員的「刪除」是一鍵動作，沒有像
// 命令列那樣「先想清楚再打指令」的天然摩擦力，誤刪的代價(尤其是刪到
// 陣列上唯一一份的資料)比大部分其他誤操作都高很多。回收桶用陣列
// 掛載點底下的一個資料夾(trashDirName)實作，不依賴任何資料庫——
// 跟 GoNAS 整體「用檔案系統本身當狀態」的設計一致，也代表回收桶的
// 內容跟陣列資料活在同一個保護傘下(SnapRAID 的同位保護一樣涵蓋
// 回收桶,誤刪的東西不會因為剛好放在「回收桶」這個資料夾就少一層保護)。
//
// 每一筆被刪除的項目搬進 <root>/.gonas-trash/<id>，並在旁邊寫一份
// <id>.meta.json 記住原始路徑跟刪除時間，Restore 靠這份 meta 資訊搬
// 回原位。刻意用「每筆一個 meta 檔案」而不是一份共用的索引檔——共用索引
// 檔需要處理併發寫入互斥的問題(跟 internal/state 的作法一樣需要一個
// mutex + 原子寫入)，對回收桶這種「寫入頻率低、每筆彼此獨立」的資料
// 有點殺雞用牛刀，每筆一個檔案天生就不會互相競爭。

// TrashEntry 是回收桶裡一筆項目的中繼資料。
type TrashEntry struct {
	ID           string    `json:"id"`
	OriginalPath string    `json:"originalPath"`
	Name         string    `json:"name"`
	IsDir        bool      `json:"isDir"`
	Size         int64     `json:"size"`
	DeletedAt    time.Time `json:"deletedAt"`
}

type trashMeta struct {
	OriginalPath string    `json:"originalPath"`
	Name         string    `json:"name"`
	IsDir        bool      `json:"isDir"`
	Size         int64     `json:"size"`
	DeletedAt    time.Time `json:"deletedAt"`
}

func trashDir(root string) string {
	return filepath.Join(root, trashDirName)
}

func moveToTrash(root, relPath, target string, info os.FileInfo) error {
	td := trashDir(root)
	if err := os.MkdirAll(td, 0o700); err != nil {
		return fmt.Errorf("preparing trash directory: %w", err)
	}

	id := fmt.Sprintf("%d-%s", time.Now().UnixNano(), sanitizeTrashName(info.Name()))
	dest := filepath.Join(td, id)

	if err := os.Rename(target, dest); err != nil {
		if errors.Is(err, syscall.EXDEV) {
			if err := moveCrossDevice(target, dest); err != nil {
				return fmt.Errorf("moving to trash: %w", err)
			}
		} else {
			return fmt.Errorf("moving to trash: %w", err)
		}
	}

	meta := trashMeta{
		OriginalPath: relPath,
		Name:         info.Name(),
		IsDir:        info.IsDir(),
		Size:         info.Size(),
		DeletedAt:    time.Now(),
	}
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding trash metadata: %w", err)
	}
	if err := os.WriteFile(filepath.Join(td, id+".meta.json"), data, 0o600); err != nil {
		return fmt.Errorf("writing trash metadata: %w", err)
	}
	return nil
}

// ListTrash 列出回收桶裡目前的所有項目，依刪除時間新到舊排序。
func ListTrash(root string) ([]TrashEntry, error) {
	td := trashDir(root)
	items, err := os.ReadDir(td)
	if err != nil {
		if os.IsNotExist(err) {
			return []TrashEntry{}, nil
		}
		return nil, fmt.Errorf("listing trash: %w", err)
	}

	var entries []TrashEntry
	for _, de := range items {
		name := de.Name()
		if !strings.HasSuffix(name, ".meta.json") {
			continue
		}
		id := strings.TrimSuffix(name, ".meta.json")
		meta, err := readTrashMeta(td, id)
		if err != nil {
			continue // meta 檔案損毀/讀不到就跳過這一筆，不影響其他項目顯示
		}
		entries = append(entries, TrashEntry{
			ID:           id,
			OriginalPath: meta.OriginalPath,
			Name:         meta.Name,
			IsDir:        meta.IsDir,
			Size:         meta.Size,
			DeletedAt:    meta.DeletedAt,
		})
	}
	// 新到舊排序,最近刪除的東西最有可能是使用者想找的。
	for i := 1; i < len(entries); i++ {
		for j := i; j > 0 && entries[j].DeletedAt.After(entries[j-1].DeletedAt); j-- {
			entries[j], entries[j-1] = entries[j-1], entries[j]
		}
	}
	if entries == nil {
		entries = []TrashEntry{}
	}
	return entries, nil
}

func readTrashMeta(td, id string) (trashMeta, error) {
	var meta trashMeta
	data, err := os.ReadFile(filepath.Join(td, id+".meta.json"))
	if err != nil {
		return meta, err
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return meta, err
	}
	return meta, nil
}

// RestoreFromTrash 把一筆回收桶項目搬回它原本的路徑。原本的位置如果已經
// 有同名的東西，拒絕執行並回傳 ErrAlreadyExists——不猜測使用者想要覆蓋
// 還是想要保留兩者，交給使用者自己先處理掉衝突再重試。原本所在的目錄如果
// 已經被刪掉，會重新建回來(MkdirAll)，不然使用者連復原都做不到。
func RestoreFromTrash(root, id string) error {
	td := trashDir(root)
	meta, err := readTrashMeta(td, id)
	if err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return fmt.Errorf("reading trash metadata: %w", err)
	}

	dest, err := resolve(root, meta.OriginalPath)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(dest); err == nil {
		return ErrAlreadyExists
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("recreating original directory: %w", err)
	}

	src := filepath.Join(td, id)
	if err := os.Rename(src, dest); err != nil {
		if errors.Is(err, syscall.EXDEV) {
			if err := moveCrossDevice(src, dest); err != nil {
				return fmt.Errorf("restoring from trash: %w", err)
			}
		} else if os.IsNotExist(err) {
			return ErrNotFound
		} else {
			return fmt.Errorf("restoring from trash: %w", err)
		}
	}
	_ = os.Remove(filepath.Join(td, id+".meta.json"))
	return nil
}

// DeleteTrashItem 永久刪除回收桶裡的單一項目(它自己的內容跟 meta 檔案)。
func DeleteTrashItem(root, id string) error {
	td := trashDir(root)
	if _, err := readTrashMeta(td, id); err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return fmt.Errorf("reading trash metadata: %w", err)
	}
	if err := os.RemoveAll(filepath.Join(td, id)); err != nil {
		return fmt.Errorf("deleting trash item: %w", err)
	}
	_ = os.Remove(filepath.Join(td, id+".meta.json"))
	return nil
}

// EmptyTrash 永久清空整個回收桶。
func EmptyTrash(root string) error {
	td := trashDir(root)
	entries, err := os.ReadDir(td)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("reading trash directory: %w", err)
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(td, e.Name())); err != nil {
			return fmt.Errorf("emptying trash: %w", err)
		}
	}
	return nil
}
