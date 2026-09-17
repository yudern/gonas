package storage

import (
	"fmt"
	"strings"

	"github.com/bng147/gonas/internal/textcheck"
)

// PoolConfig 描述一個「Unraid 式」儲存池:每顆資料硬碟各自獨立掛載、
// 保留原始檔案系統(建議 XFS 或 BTRFS),不做傳統 RAID 條帶化;
// mergerFS 把它們聯合成一個掛載點,SnapRAID 則在背景提供非即時同位校驗。
//
// 這與 ZFS/mdadm 的關鍵差異、也是使用者選擇這個方案的原因:任何一顆資料碟
// 拔出來,用一般的 xfs/btrfs 工具就能直接讀到上面的檔案,不需要整個陣列
// 都在線才能救資料。
type PoolConfig struct {
	// Name 是這個池的識別名稱,同時會拿來當 mergerFS 掛載點與 SnapRAID
	// 設定檔的檔名(例如 "tank" -> /mnt/tank, /etc/gonas/snapraid-tank.conf)。
	Name string `json:"name"`

	// DataDisks 是資料碟的掛載點(例如 "/mnt/disk1", "/mnt/disk2", ...),
	// 每一個都必須是已經格式化、已經掛載好的獨立檔案系統。
	// Phase 1 先假設磁碟已由使用者(或未來 Phase 4 的精靈)手動格式化掛載好,
	// 「自動幫使用者格式化硬碟」是之後才要做、且需要額外確認機制的危險操作。
	DataDisks []string `json:"dataDisks"`

	// ParityDisks 是同位校驗碟的掛載點。SnapRAID 最多支援 6 顆同位碟
	// (對應最多可同時容忍 6 顆資料碟故障),多數家用場景 1-2 顆就夠。
	ParityDisks []string `json:"parityDisks"`

	// MountPoint 是 mergerFS 聯合掛載後,使用者實際存取檔案的路徑,
	// 例如 "/mnt/tank"。這才是 Samba/NFS/Docker volume 要指向的地方。
	MountPoint string `json:"mountPoint"`

	// ContentFiles 是 SnapRAID 用來記錄「哪個檔案在哪顆碟」的索引檔位置,
	// 依慣例建議放在陣列以外的可靠儲存(如系統碟),且至少 2 份互為備份。
	ContentFiles []string `json:"contentFiles"`
}

// Validate 檢查設定是否足以啟動陣列。刻意寫得嚴格一點:寧可拒絕啟動、
// 也不要用一個有問題的設定去掛載或跑校驗 —— 這一層一旦出錯，代價是使用者的資料。
func (c PoolConfig) Validate() error {
	if c.Name == "" {
		return fmt.Errorf("pool name is required")
	}
	if len(c.DataDisks) == 0 {
		return fmt.Errorf("pool %q: at least one data disk is required", c.Name)
	}
	if len(c.ParityDisks) == 0 {
		return fmt.Errorf("pool %q: at least one parity disk is required (unprotected pools are not supported by design)", c.Name)
	}
	if c.MountPoint == "" {
		return fmt.Errorf("pool %q: mountPoint is required", c.Name)
	}
	if len(c.ContentFiles) < 2 {
		return fmt.Errorf("pool %q: at least 2 content file locations are recommended so the SnapRAID index itself isn't a single point of failure", c.Name)
	}

	// 第三十四輪(設定產生器注入稽核收尾):snapraid.conf 由 text/template
	// 產生(不跳脫換行)、且是 line-based、以空白分隔(例如 `data d1 <path>`)。
	// 名稱/路徑含換行會注入指令,含空白會拆錯欄位,所以這些都不能有控制
	// 字元或空白。見 internal/textcheck。
	if textcheck.HasControl(c.Name) || strings.ContainsAny(c.Name, " \t/") {
		return fmt.Errorf("pool %q: name cannot contain spaces, slashes, line breaks, or control characters", c.Name)
	}
	pathFields := append(append(append([]string{c.MountPoint}, c.DataDisks...), c.ParityDisks...), c.ContentFiles...)
	for _, pth := range pathFields {
		if textcheck.HasControl(pth) || strings.ContainsAny(pth, " \t") {
			return fmt.Errorf("pool %q: path %q cannot contain spaces, line breaks, or control characters (breaks snapraid.conf format)", c.Name, pth)
		}
	}

	seen := make(map[string]bool, len(c.DataDisks)+len(c.ParityDisks))
	for _, d := range c.DataDisks {
		if d == "" {
			return fmt.Errorf("pool %q: data disk path cannot be empty", c.Name)
		}
		if seen[d] {
			return fmt.Errorf("pool %q: disk %q listed more than once", c.Name, d)
		}
		seen[d] = true
	}
	for _, p := range c.ParityDisks {
		if p == "" {
			return fmt.Errorf("pool %q: parity disk path cannot be empty", c.Name)
		}
		if seen[p] {
			return fmt.Errorf("pool %q: disk %q used as both data and parity, or listed twice", c.Name, p)
		}
		seen[p] = true
	}

	return nil
}
