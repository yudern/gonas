package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/bng147/gonas/internal/textcheck"
)

// deviceRe 限制「準備硬碟」只接受一顆整碟的裝置路徑,例如 /dev/sdb、
// /dev/nvme0n1。刻意收得很窄:只允許 /dev/ 後面接小寫字母與數字,不含
// 空白、斜線、標點、shell 特殊字元——這一層本身就擋掉了指令注入
// (device 之後會被當成參數餵給 mkfs/mount)。
var deviceRe = regexp.MustCompile(`^/dev/[a-z0-9]+$`)

// DefaultFstabPath 是正式環境寫入開機自動掛載設定的檔案。測試會傳自己的
// 暫存檔路徑進來,不去動真的 /etc/fstab。
const DefaultFstabPath = "/etc/fstab"

// PrepareResult 回報「準備硬碟」實際做了什麼,給 API/前端顯示。
type PrepareResult struct {
	Device     string `json:"device"`
	Mountpoint string `json:"mountpoint"`
	FSType     string `json:"fsType"`
	UUID       string `json:"uuid"`
}

// PrepareDisk 把使用者「明確選定」的一顆整碟格式化成 ext4、掛載到
// mountpoint,並寫入 fstab 讓重開機後自動掛回。這是破壞性操作(會清空該
// 碟),所以層層設防:
//   - device 必須是合法的整碟路徑(deviceRe),擋注入。
//   - mountpoint 必須在 /mnt/ 底下、無空白/控制字元/".."(擋掛到系統目錄、
//     擋 fstab 格式被破壞)。
//   - 動手前先用 lsblk 檢查這顆碟(含其分割區)有沒有任何一個正被掛載;
//     只要有就拒絕——這一條同時保護了系統碟(它的分割區掛在 / 與 /boot)。
//
// GoNAS 永遠不會自動挑碟或自動格式化:一定是使用者在網頁上選了某顆特定的
// 碟、明確按下確認,才會走到這裡。
func PrepareDisk(ctx context.Context, r Runner, device, mountpoint, fstabPath string) (PrepareResult, error) {
	var res PrepareResult
	if !deviceRe.MatchString(device) {
		// 固定英文句子(不內嵌 device 值)以便前端 errorMap 對照翻譯,理由
		// 見 storage/pool.go Validate 的說明。
		return res, errors.New("invalid disk device — choose a whole disk such as /dev/sdb")
	}
	if err := validateMountpoint(mountpoint); err != nil {
		return res, err
	}
	if fstabPath == "" {
		fstabPath = DefaultFstabPath
	}

	// 動手前先用 lsblk 檢查這顆裝置:必須是「整碟」(type==disk,不是分割
	// 區),而且它本身與其上任何分割區都不能正被掛載。掛載檢查同時擋掉
	// 「不小心選到系統碟」——系統碟的分割區掛在 / 和 /boot。
	isDisk, mps, err := inspectDevice(ctx, r, device)
	if err != nil {
		return res, err
	}
	if !isDisk {
		return res, errors.New("that device is not a whole disk (it may be a partition) — pick a whole disk such as /dev/sdb")
	}
	if len(mps) > 0 {
		return res, errors.New("refusing to format: the disk (or a partition on it) is currently mounted — GoNAS never formats an in-use disk, including the system disk")
	}

	// 第五十三輪(真機測試抓到):目標掛載點必須是空閒的,不能已經有別的碟
	// 掛在上面。Linux 的 mount 允許「堆疊掛載」——把第二顆碟掛到已經有碟的
	// /mnt/disk1,不會報錯,但後掛的會蓋住先掛的,寫進去的資料進了看不到的
	// 那一層;而且 appendFstabEntry 對同一個掛載點是冪等的,只會留第一筆,
	// 重開機後只有一顆掛得回來。結果是儲存池把「同一個掛載點」收到不只一次
	// (使用者看到的『同一顆碟被列了不止一次』其實是這個),更糟的是靜默
	// 資料遺失。所以動手 mkfs 之前先擋掉。
	if mountpointBusy(ctx, r, mountpoint) {
		return res, errors.New("that mount point is already in use by another disk — pick a different one such as /mnt/disk2")
	}

	// 格式化成 ext4(SnapRAID 的資料碟/校驗碟直接用整碟檔案系統,不需要
	// 額外切分割表)。-F 不互動詢問、-q 安靜輸出。
	if _, err := r.Run(ctx, "mkfs.ext4", "-F", "-q", device); err != nil {
		return res, fmt.Errorf("formatting %s as ext4 failed: %w", device, err)
	}

	if _, err := r.Run(ctx, "mkdir", "-p", mountpoint); err != nil {
		return res, fmt.Errorf("creating mount point %s failed: %w", mountpoint, err)
	}
	if _, err := r.Run(ctx, "mount", device, mountpoint); err != nil {
		return res, fmt.Errorf("mounting %s at %s failed: %w", device, mountpoint, err)
	}

	// 取 UUID 寫進 fstab,比用 /dev/sdX 這種會隨插拔順序變動的名稱穩。
	uuidOut, err := r.Run(ctx, "blkid", "-s", "UUID", "-o", "value", device)
	if err != nil {
		return res, fmt.Errorf("reading UUID of %s failed: %w", device, err)
	}
	uuid := strings.TrimSpace(string(uuidOut))
	if uuid != "" {
		if err := appendFstabEntry(fstabPath, uuid, mountpoint); err != nil {
			return res, fmt.Errorf("updating %s failed: %w", fstabPath, err)
		}
	}

	return PrepareResult{Device: device, Mountpoint: mountpoint, FSType: "ext4", UUID: uuid}, nil
}

// mountpointBusy 回報 mp 這個掛載點目前是不是已經有東西掛在上面。用
// findmnt 精準比對(--mountpoint 是「剛好掛在這個路徑」而不是「這個路徑
// 底下」);findmnt 對「沒有東西掛在那裡」會以非零狀態結束,Runner 把它
// 變成 err,這裡就當作空閒。findmnt 屬 util-linux,Debian 一定有。
func mountpointBusy(ctx context.Context, r Runner, mp string) bool {
	out, err := r.Run(ctx, "findmnt", "-rno", "TARGET", "--mountpoint", mp)
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) != ""
}

func validateMountpoint(mp string) error {
	if !strings.HasPrefix(mp, "/mnt/") || len(mp) <= len("/mnt/") {
		return errors.New("the mount point must be under /mnt/ (for example /mnt/disk1)")
	}
	if textcheck.HasControl(mp) || strings.ContainsAny(mp, " \t") {
		return errors.New("the mount point cannot contain spaces or control characters")
	}
	if strings.Contains(mp, "..") {
		return errors.New("the mount point cannot contain '..'")
	}
	return nil
}

// inspectDevice 用 lsblk 看一顆裝置:回傳它是不是「整碟」(top-level
// type==disk),以及它本身與其所有分割區目前的掛載點(空字串不算)。
func inspectDevice(ctx context.Context, r Runner, device string) (isDisk bool, mountpoints []string, err error) {
	out, err := r.Run(ctx, "lsblk", "-J", "-o", "PATH,TYPE,MOUNTPOINT", device)
	if err != nil {
		return false, nil, fmt.Errorf("inspecting %s failed: %w", device, err)
	}
	var parsed lsblkOutput
	if err := json.Unmarshal(out, &parsed); err != nil {
		return false, nil, fmt.Errorf("parsing lsblk output for %s failed: %w", device, err)
	}
	var mps []string
	var walk func(d lsblkDevice)
	walk = func(d lsblkDevice) {
		if strings.TrimSpace(d.Mountpoint) != "" {
			mps = append(mps, d.Mountpoint)
		}
		for _, c := range d.Children {
			walk(c)
		}
	}
	for _, d := range parsed.BlockDevices {
		walk(d)
	}
	if len(parsed.BlockDevices) > 0 && parsed.BlockDevices[0].Type == "disk" {
		isDisk = true
	}
	return isDisk, mps, nil
}

// appendFstabEntry 冪等地把一筆 UUID 掛載寫進 fstab:同一個 UUID 或同一個
// 掛載點已經有紀錄就不重複寫。
func appendFstabEntry(fstabPath, uuid, mountpoint string) error {
	existing, err := os.ReadFile(fstabPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, l := range strings.Split(string(existing), "\n") {
		f := strings.Fields(l)
		if len(f) >= 2 && (f[0] == "UUID="+uuid || f[1] == mountpoint) {
			return nil // 已存在,冪等跳過
		}
	}
	body := string(existing)
	if len(body) > 0 && !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	body += "# added by GoNAS prepare-disk\n"
	body += fmt.Sprintf("UUID=%s %s ext4 defaults 0 2\n", uuid, mountpoint)
	return writeFileAtomic(fstabPath, []byte(body), 0o644)
}

// writeFileAtomic 把 data 寫進 path:先寫同目錄暫存檔、fsync、再 rename。
// 第五十六輪覆核(QA2):/etc/fstab 原本用 os.WriteFile 直接就地覆寫,斷電時
// (正是這台 NAS 的 UPS 要處理的事件)可能留下截斷/空的 fstab,下次開機資料碟
// (甚至系統掛載)掛不起來。改成 temp+fsync+rename,跟 state/samba 的原子寫入
// 一致。刻意不 import internal/share(避免 storage→share 相依),就地寫一份。
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".gonas-fstab-tmp-*")
	if err != nil {
		return fmt.Errorf("creating temp file in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("writing %s: %w", tmpPath, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("syncing %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", tmpPath, err)
	}
	if err := os.Chmod(tmpPath, perm); err != nil {
		return fmt.Errorf("chmod %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("renaming %s to %s: %w", tmpPath, path, err)
	}
	if d, derr := os.Open(dir); derr == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
