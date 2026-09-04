package storage

import (
	"context"
	"fmt"
	"strings"
)

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
	if _, err := r.Run(ctx, "mergerfs", BuildMergerfsArgs(cfg)...); err != nil {
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
