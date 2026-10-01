package backup

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bng147/gonas/internal/cmdrunner"
)

// RestoreResult 是一次還原的結果摘要。
type RestoreResult struct {
	SnapshotName string `json:"snapshotName"`
	TargetPath   string `json:"targetPath"`
	Success      bool   `json:"success"`
	Error        string `json:"error,omitempty"`
}

// RunRestore 把某一份快照的內容還原到 targetPath。第六十輪產品覆核(P0):
// 先前備份能建、能列、卻沒有任何「還原」的路徑——一份還原不了的備份不算備份。
//
// 設計上刻意保守,因為還原是把資料「寫回去」的動作,寫錯地方或帶了 --delete
// 可能反而毀掉使用者現有的檔案:
//   - 用 rsync -aAX 從 <快照目錄>/ 複製到 targetPath/,**不帶 --delete**——
//     還原是「把這份快照的檔案放回來」,不該去刪 targetPath 既有的其他檔案。
//     真要「完全還原成快照當時的樣子」是另一種更危險的語意,不做預設。
//   - targetPath 必須是絕對路徑;不允許還原到快照/備份目的地自己底下(那會
//     造成遞迴複製、把備份寫花)。
//   - 找不到指定快照就明確回錯,不會靜默地還原成別的東西。
//
// snapshotName 是 ListSnapshots 回傳的 Name(內部時間戳記目錄名)。targetPath
// 由呼叫端決定:常見是還原回原本的來源目錄,或還原到一個新目錄再自己搬。
func RunRestore(ctx context.Context, r cmdrunner.Runner, job Job, snapshotName, targetPath string) RestoreResult {
	res := RestoreResult{SnapshotName: snapshotName, TargetPath: targetPath}

	if err := job.Validate(); err != nil {
		return failRestore(res, fmt.Errorf("invalid backup job: %w", err))
	}
	if strings.TrimSpace(snapshotName) == "" {
		return failRestore(res, fmt.Errorf("no snapshot specified"))
	}
	if !filepath.IsAbs(targetPath) {
		return failRestore(res, fmt.Errorf("restore target must be an absolute path"))
	}
	// 擋掉把資料還原進備份目的地底下(會遞迴、污染備份)。
	if pathContainsOrEqual(job.DestPath, targetPath) {
		return failRestore(res, fmt.Errorf("restore target must not be inside the backup destination (%s)", job.DestPath))
	}
	// 第六十輪安全複審:還原是「以 root 把(可能來自共享、內容由使用者掌控的)
	// 快照檔案寫到 targetPath」。若不設限,管理者(或被盜用的管理者 session)可
	// 以還原到 /etc、/root、/boot… 覆寫系統檔。擋掉這些危險的系統目錄根——還原
	// 的正當目標是資料區(/mnt/...)或原本的來源目錄,不會是這些。
	if isSystemPath(targetPath) {
		return failRestore(res, fmt.Errorf("refusing to restore into a system directory (%s) — choose a path on your data array", targetPath))
	}

	// 確認指定的快照真的存在(且是這個 Job 底下已完成的快照),不接受呼叫端
	// 傳進來的任意目錄名——避免 "../" 之類的路徑穿越。
	snaps, err := listSnapshots(job)
	if err != nil {
		return failRestore(res, fmt.Errorf("listing snapshots: %w", err))
	}
	found := false
	for _, n := range snaps {
		if n == snapshotName {
			found = true
			break
		}
	}
	if !found {
		return failRestore(res, fmt.Errorf("snapshot %q not found for this job", snapshotName))
	}

	snapshotDir := filepath.Join(jobDir(job), snapshotName)

	// 目標目錄不存在就建出來(還原到一個新位置是常見用法)。
	if err := os.MkdirAll(targetPath, 0o755); err != nil {
		return failRestore(res, fmt.Errorf("ensuring restore target directory: %w", err))
	}

	// -aAX 保留權限/ACL/xattr;不帶 --delete(理由見函式說明)。來源加尾斜線,
	// 把「快照內容」複製進 targetPath,而不是把快照目錄本身塞進去。
	args := []string{"-aAX", ensureTrailingSlash(snapshotDir), ensureTrailingSlash(targetPath)}
	if _, err := r.Run(ctx, "rsync", args...); err != nil {
		return failRestore(res, fmt.Errorf("rsync restore failed: %w", err))
	}

	res.Success = true
	return res
}

// isSystemPath 回報 targetPath 是不是落在(或就是)作業系統的關鍵目錄,
// 這些地方不該被還原覆寫。比對前先 Clean,避免用 "/etc/../etc" 之類繞過。
func isSystemPath(targetPath string) bool {
	clean := filepath.Clean(targetPath)
	if clean == "/" {
		return true
	}
	systemRoots := []string{"/etc", "/root", "/boot", "/bin", "/sbin", "/lib", "/lib64", "/usr", "/var", "/sys", "/proc", "/dev", "/run"}
	for _, root := range systemRoots {
		if clean == root || strings.HasPrefix(clean, root+"/") {
			return true
		}
	}
	return false
}

func failRestore(res RestoreResult, err error) RestoreResult {
	res.Success = false
	res.Error = err.Error()
	return res
}
