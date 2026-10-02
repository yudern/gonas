package backup

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/bng147/gonas/internal/cmdrunner"
)

// RunBackup 執行一次完整的備份:組出 rsync 指令(如果有既有快照就帶
// --link-dest 做硬連結增量)、複製到一個 .partial 暫存目錄、成功後
// promote 成正式的時間戳記快照、更新 latest symlink,最後清掉超過
// RetentionCount 的舊快照。
//
// now 由呼叫端傳入而不是內部呼叫 time.Now() —— 讓測試可以餵固定的時間
// 產生可預期的快照目錄名稱,不需要在斷言裡處理「執行當下究竟是幾點幾分」
// 這種不確定性。
func RunBackup(ctx context.Context, r cmdrunner.Runner, job Job, now time.Time) RunResult {
	result := RunResult{StartedAt: now}

	if err := job.Validate(); err != nil {
		return failResult(result, fmt.Errorf("invalid backup job: %w", err))
	}
	// 異地鏡像(SSH):走完全不同的一條路(rsync over ssh、無本機快照輪替)。
	if job.IsRemote() {
		return runRemoteMirror(ctx, r, job, now)
	}
	if err := os.MkdirAll(jobDir(job), 0o750); err != nil {
		return failResult(result, fmt.Errorf("ensuring backup destination directory: %w", err))
	}

	tmpDir := jobDir(job) + "/" + snapshotName(now) + partialSuffix
	// 保險清掉上次可能因為 daemon 被強制中斷而留下的同名殘留目錄 —— 理論上
	// 兩次執行的時間戳記精確到秒,撞名機率極低,但 rsync 對著一個已經存在、
	// 內容是上次失敗殘留的目錄執行,結果會是難以預期的部分狀態,不如直接
	// 清乾淨重新開始。
	if err := os.RemoveAll(tmpDir); err != nil {
		return failResult(result, fmt.Errorf("clearing stale partial snapshot: %w", err))
	}

	linkDest, err := resolveLinkDest(job)
	if err != nil {
		// 讀不到 latest symlink 不該讓整次備份失敗 —— 退化成沒有
		// --link-dest 的完整複製,頂多是這一次多佔一些磁碟空間,好過
		// 直接放棄備份。
		linkDest = ""
	}

	args := []string{"-aAX", "--delete"}
	if linkDest != "" {
		args = append(args, "--link-dest="+linkDest)
	}
	args = append(args, ensureTrailingSlash(job.SourcePath), tmpDir)

	if _, err := r.Run(ctx, "rsync", args...); err != nil {
		_ = os.RemoveAll(tmpDir)
		return failResult(result, fmt.Errorf("rsync failed: %w", err))
	}

	snapshotDir, err := promoteSnapshot(job, tmpDir, now)
	if err != nil {
		return failResult(result, err)
	}
	result.SnapshotDir = snapshotDir

	// 清理舊快照失敗不影響「這次備份本身有沒有成功」的判斷 —— 資料已經
	// 安全複製完成,只是多保留了幾份超過設定值的快照,不是資料遺失風險,
	// 沒有理由讓使用者看到這次備份整個標記成失敗。
	if err := pruneSnapshots(job); err != nil {
		result.FinishedAt = time.Now()
		result.Success = true
		result.Error = "備份成功,但清理舊快照時發生錯誤:" + err.Error()
		return result
	}

	result.FinishedAt = time.Now()
	result.Success = true
	return result
}

// buildRemoteSSHOpt 組出要傳給 rsync `-e` 的 ssh 指令字串。只用金鑰認證:
// BatchMode=yes 讓它在需要密碼/互動時直接失敗而不是卡住;StrictHostKeyChecking=
// accept-new 是「首次信任」(避免第一次連線卡在 host key 確認),對使用者自己的
// 遠端主機是合理預設;ConnectTimeout 避免遠端不通時吊很久。
func buildRemoteSSHOpt(rd *RemoteDest) string {
	port := rd.Port
	if port == 0 {
		port = 22
	}
	opt := fmt.Sprintf("ssh -p %d -o BatchMode=yes -o StrictHostKeyChecking=accept-new -o ConnectTimeout=20", port)
	if rd.SSHKey != "" {
		opt += " -i " + rd.SSHKey // 已在 Validate 擋掉空白/特殊字元
	}
	return opt
}

// runRemoteMirror 把來源用 rsync over SSH 鏡像到遠端目錄(含 --delete,遠端成為
// 來源的即時鏡像)。這是「異地備份」——資料離開本機,防火災/失竊。不做快照
// 輪替(那需要在遠端跑一堆 mkdir/mv/ln,複雜且脆弱);要版本化就在本機另外
// 開一個本機快照備份。
func runRemoteMirror(ctx context.Context, r cmdrunner.Runner, job Job, now time.Time) RunResult {
	result := RunResult{StartedAt: now}
	rd := job.Remote
	target := rd.User + "@" + rd.Host + ":" + ensureTrailingSlash(rd.Path)
	args := []string{"-aAX", "--delete", "-e", buildRemoteSSHOpt(rd), ensureTrailingSlash(job.SourcePath), target}
	if _, err := r.Run(ctx, "rsync", args...); err != nil {
		return failResult(result, fmt.Errorf("remote mirror rsync failed (check the host, SSH key, and that the remote path exists): %w", err))
	}
	result.FinishedAt = time.Now()
	result.Success = true
	return result
}

func failResult(result RunResult, err error) RunResult {
	result.FinishedAt = time.Now()
	result.Success = false
	result.Error = err.Error()
	return result
}

// ensureTrailingSlash 確保來源路徑以 "/" 結尾 —— 這是 rsync 的經典陷阱:
// `rsync src dst` 會把 src 這個目錄本身複製到 dst 底下(變成
// dst/src/...),`rsync src/ dst` 才是把 src 的內容複製進 dst
// (dst/...)。GoNAS 的快照目錄設計成「快照目錄本身就是來源內容的根」,
// 所以一律需要後者的行為。
func ensureTrailingSlash(path string) string {
	if strings.HasSuffix(path, "/") {
		return path
	}
	return path + "/"
}
