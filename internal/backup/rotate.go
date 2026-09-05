package backup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// partialSuffix 標記一個還沒完成的快照目錄。rsync 執行中如果被中斷
// (daemon 重啟、指令失敗),留在磁碟上的目錄名稱一律帶這個後綴 ——
// listSnapshots/pruneSnapshots 看到這個後綴的目錄一律跳過,不會誤把
// 一份寫到一半的備份當成可以拿來 --link-dest 的完整快照,也不會被
// 誤算進「保留幾份快照」的計數裡。
const partialSuffix = ".partial"

// latestLinkName 是指向最近一次成功快照的 symlink 名稱,放在
// jobDir 底下,給下一次執行時的 --link-dest 用。
const latestLinkName = "latest"

// snapshotTimeFormat 決定快照目錄的命名格式。用這個格式而不是 RFC3339
// 是因為 RFC3339 裡的 ":" 在部分檔案系統/工具(尤其是可能透過 SMB
// 分享出去給 Windows 用戶端看的備份目的地)是不合法或容易出問題的檔名
// 字元。這個格式的字串排序順序剛好等於時間先後順序,pruneSnapshots
// 排序時可以直接比字串,不需要把每個目錄名稱都反解析回 time.Time。
const snapshotTimeFormat = "20060102-150405"

// jobDir 是這個 Job 在 DestPath 底下專屬的子目錄。用 Job.ID(而不是
// Job.Name)當目錄名稱,是因為名稱可以改、ID 不會變 —— 雖然目前 GoNAS
// 沒有提供改名稱的 API,但這樣設計之後要加也不會牽動到已經存在磁碟上的
// 快照。多個 Job 就算填了同一個 DestPath 根目錄,也會各自落在獨立的子
// 目錄下,不會互相干擾或誤刪對方的快照。
func jobDir(job Job) string {
	return filepath.Join(job.DestPath, job.ID)
}

func latestLinkPath(job Job) string {
	return filepath.Join(jobDir(job), latestLinkName)
}

func snapshotName(at time.Time) string {
	return at.UTC().Format(snapshotTimeFormat)
}

// resolveLinkDest 讀取「latest」symlink 指到哪個既有快照目錄,給
// RunBackup 組 `rsync --link-dest=...` 參數用。第一次執行、還沒有任何
// 快照時回傳空字串(不是錯誤)——呼叫端看到空字串就不加 --link-dest,
// 直接做一次完整複製,這是完全正常、預期中的情況。
func resolveLinkDest(job Job) (string, error) {
	target, err := os.Readlink(latestLinkPath(job))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("reading latest snapshot link: %w", err)
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(jobDir(job), target)
	}
	return target, nil
}

// listSnapshots 列出這個 Job 底下所有已經成功完成的快照目錄名稱(排除
// latest symlink 本身跟任何殘留的 .partial 目錄),由舊到新排序 ——
// snapshotTimeFormat 的字串排序恰好等於時間排序,不需要額外解析。
// jobDir 還不存在(這個 Job 從來沒執行過)回傳空清單而不是錯誤,呼叫端
// 不用另外判斷「第一次」這個特例。
func listSnapshots(job Job) ([]string, error) {
	entries, err := os.ReadDir(jobDir(job))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("listing snapshots for job %s: %w", job.ID, err)
	}

	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			continue // 跳過 latest symlink(在大多數檔案系統上 DirEntry.IsDir() 對 symlink 一律回 false,不會被誤判成快照)
		}
		name := e.Name()
		if filepath.Ext(name) == partialSuffix {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// SnapshotInfo 描述一份已完成的快照,給 internal/api 這種需要對外呈現
// 人類可讀時間的呼叫端使用 —— 呼叫端不需要知道內部快照目錄的命名格式。
type SnapshotInfo struct {
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
}

// ListSnapshots 是 listSnapshots 的匯出版本,多做一步把目錄名稱(內部的
// snapshotTimeFormat 格式)解析回 time.Time。
func ListSnapshots(job Job) ([]SnapshotInfo, error) {
	names, err := listSnapshots(job)
	if err != nil {
		return nil, err
	}
	infos := make([]SnapshotInfo, 0, len(names))
	for _, name := range names {
		at, err := time.Parse(snapshotTimeFormat, name)
		if err != nil {
			at = time.Time{} // 理論上不會發生(名稱是 GoNAS 自己產生的),防禦性地給零值而不是讓整個列表 API 失敗
		} else {
			at = at.UTC()
		}
		infos = append(infos, SnapshotInfo{Name: name, CreatedAt: at})
	}
	return infos, nil
}

// promoteSnapshot 把剛完成的暫存目錄(帶 .partial 後綴)重新命名成正式的
// 時間戳記快照目錄,並把 latest symlink 指過去。用 os.Rename 而不是
// 複製再刪除舊目錄,確保這一步是原子的 —— 不會有「重新命名到一半」
// 這種中間狀態讓下一次執行的 --link-dest 或 listSnapshots 看到損毀的
// 目錄。
func promoteSnapshot(job Job, tmpDir string, at time.Time) (string, error) {
	finalDir := filepath.Join(jobDir(job), snapshotName(at))
	if err := os.Rename(tmpDir, finalDir); err != nil {
		return "", fmt.Errorf("promoting snapshot: %w", err)
	}

	linkPath := latestLinkPath(job)
	tmpLink := linkPath + ".tmp"
	_ = os.Remove(tmpLink) // 保險:上次萬一在這一步中斷留下的殘留檔案
	if err := os.Symlink(finalDir, tmpLink); err != nil {
		return finalDir, fmt.Errorf("creating latest symlink: %w", err)
	}
	// 用「先建一個暫時的 symlink、再 rename 蓋過去」而不是直接
	// os.Remove(linkPath) 後 os.Symlink(linkPath),是為了不要有一瞬間
	// latest symlink 完全不存在的空窗期 —— os.Rename 對同一個檔案系統上
	// 的 symlink 一樣是原子操作。
	if err := os.Rename(tmpLink, linkPath); err != nil {
		return finalDir, fmt.Errorf("updating latest symlink: %w", err)
	}
	return finalDir, nil
}

// pruneSnapshots 刪掉超過 Job.RetentionCount 的最舊快照(latest symlink
// 如果剛好指到被刪除的快照,不會另外處理懸空的問題,因為呼叫端(RunBackup)
// 一律在成功執行「新一次」備份、promoteSnapshot 更新過 latest 之後才呼叫
// pruneSnapshots,被刪掉的一定不會是目前的 latest)。
func pruneSnapshots(job Job) error {
	names, err := listSnapshots(job)
	if err != nil {
		return err
	}
	if len(names) <= job.RetentionCount {
		return nil
	}
	toRemove := names[:len(names)-job.RetentionCount]
	for _, name := range toRemove {
		if err := os.RemoveAll(filepath.Join(jobDir(job), name)); err != nil {
			return fmt.Errorf("pruning old snapshot %s: %w", name, err)
		}
	}
	return nil
}
