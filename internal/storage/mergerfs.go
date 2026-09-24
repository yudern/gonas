package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
)

// ErrMergerfsNotInstalled 是固定英文錯誤(前端 errorMap 翻譯):要啟動儲存池
// 需要 mergerfs 這個外部程式,但它還沒安裝。第五十五輪(使用者實機):離線
// NAS 上 mergerfs 沒裝,啟動陣列時 mergerfs 的 exec 直接回「executable file
// not found in $PATH」——那對使用者是天書。這裡把它換成一句看得懂、知道下一步
// 怎麼做的話。
var ErrMergerfsNotInstalled = errors.New("mergerfs is not installed — the storage pool needs it to combine your data disks; install it from the System Doctor page (or it comes preinstalled on the offline appliance image)")

// looksLikeMissingBinary 判斷一個 exec 錯誤是不是「找不到執行檔」。os/exec
// 在 PATH 裡找不到程式時,錯誤字串會含「executable file not found」;有些
// 情況(檔案在但不可執行/路徑怪)會是「no such file or directory」。
func looksLikeMissingBinary(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "executable file not found") ||
		strings.Contains(s, "no such file or directory")
}

// mergerfsOptions 是預設掛載選項,對應 Unraid 式的使用習慣:
//   - func.create=mfs           新檔案寫到「剩餘空間最多」的那顆碟(Most Free Space),
//     讓各碟用量盡量平均,而不是塞爆同一顆再換下一顆。
//   - category.create=mfs       目錄建立也套用同樣策略。
//   - dropcacheonclose=true     檔案關閉後釋放 page cache,NAS 長時間掛載大量檔案
//     時避免記憶體被 cache 吃滿。
//   - moveonenospc=true         某顆碟寫到滿時,自動把檔案搬去別顆碟繼續寫完,
//     而不是直接回傳「磁碟已滿」錯誤。
const mergerfsOptions = "cache.files=partial,dropcacheonclose=true,category.create=mfs,func.create=mfs,moveonenospc=true"

// BuildMergerfsArgs 組出呼叫 mergerfs 指令所需的參數。回傳參數而不是直接組
// 成一個字串,是為了讓 execRunner / 假 Runner 都能拿到乾淨的 []string,
// 不用擔心路徑裡有空白之類的 shell 轉義問題。
func BuildMergerfsArgs(cfg PoolConfig) []string {
	branches := strings.Join(cfg.DataDisks, ":")
	return []string{
		"-o", mergerfsOptions,
		branches,
		cfg.MountPoint,
	}
}

// MountPool 呼叫 mergerfs 把 cfg.DataDisks 聯合掛載到 cfg.MountPoint。
// 呼叫前必須先確定 cfg.MountPoint 已存在且各 DataDisks 已各自掛載好。
func MountPool(ctx context.Context, r Runner, cfg PoolConfig) error {
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("refusing to mount invalid pool config: %w", err)
	}
	// 第五十五輪覆核(產品 P2):聯合掛載點目錄不存在的話,mergerfs 會直接失敗
	// (「mergerfs mount failed: ... No such file or directory」)。準備硬碟那步
	// 只會 mkdir 各顆資料碟的 /mnt/diskN,不會建這個聯合掛載點(/mnt/tank),
	// 所以這裡在掛載前先把它建出來。0o755:一般人也讀得到、只有 root 能改。
	if err := os.MkdirAll(cfg.MountPoint, 0o755); err != nil {
		return fmt.Errorf("creating pool mount point %q failed: %w", cfg.MountPoint, err)
	}
	if _, err := r.Run(ctx, "mergerfs", BuildMergerfsArgs(cfg)...); err != nil {
		if looksLikeMissingBinary(err) {
			// 回固定英文句子(不含 %w 包裝),讓前端 errorMap 對得到、翻成
			// 看得懂的提示,而不是把 exec 的原始英文錯誤丟出去。
			return ErrMergerfsNotInstalled
		}
		return fmt.Errorf("mergerfs mount failed: %w", err)
	}
	return nil
}

// UnmountPool 卸載 mergerfs 聯合掛載點。用 fusermount -uz(lazy unmount)
// 而不是直接 umount,是因為 mergerfs 是 FUSE 檔案系統,fusermount 是官方
// 建議的卸載方式,-z 可以避免「還有程式占用著」導致卸載失敗。
func UnmountPool(ctx context.Context, r Runner, cfg PoolConfig) error {
	if _, err := r.Run(ctx, "fusermount", "-uz", cfg.MountPoint); err != nil {
		return fmt.Errorf("unmounting pool %q at %q failed: %w", cfg.Name, cfg.MountPoint, err)
	}
	return nil
}
