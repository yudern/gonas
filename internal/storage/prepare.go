package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
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
		return res, fmt.Errorf("不接受的裝置路徑 %q:必須是一顆整碟,例如 /dev/sdb", device)
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
		return res, fmt.Errorf("%s 不是一顆整碟(可能是分割區或其他裝置);請選整顆硬碟,例如 /dev/sdb", device)
	}
	if len(mps) > 0 {
		return res, fmt.Errorf("拒絕格式化 %s:它(或其上的分割區)目前正掛載於 %s ——GoNAS 絕不格式化使用中的碟(系統碟也在此列)", device, strings.Join(mps, ", "))
	}

	// 格式化成 ext4(SnapRAID 的資料碟/校驗碟直接用整碟檔案系統,不需要
	// 額外切分割表)。-F 不互動詢問、-q 安靜輸出。
	if _, err := r.Run(ctx, "mkfs.ext4", "-F", "-q", device); err != nil {
		return res, fmt.Errorf("將 %s 格式化為 ext4 失敗: %w", device, err)
	}

	if _, err := r.Run(ctx, "mkdir", "-p", mountpoint); err != nil {
		return res, fmt.Errorf("建立掛載點 %s 失敗: %w", mountpoint, err)
	}
	if _, err := r.Run(ctx, "mount", device, mountpoint); err != nil {
		return res, fmt.Errorf("將 %s 掛載到 %s 失敗: %w", device, mountpoint, err)
	}

	// 取 UUID 寫進 fstab,比用 /dev/sdX 這種會隨插拔順序變動的名稱穩。
	uuidOut, err := r.Run(ctx, "blkid", "-s", "UUID", "-o", "value", device)
	if err != nil {
		return res, fmt.Errorf("讀取 %s 的 UUID 失敗: %w", device, err)
	}
	uuid := strings.TrimSpace(string(uuidOut))
	if uuid != "" {
		if err := appendFstabEntry(fstabPath, uuid, mountpoint); err != nil {
			return res, fmt.Errorf("更新 %s 失敗: %w", fstabPath, err)
		}
	}

	return PrepareResult{Device: device, Mountpoint: mountpoint, FSType: "ext4", UUID: uuid}, nil
}

func validateMountpoint(mp string) error {
	if !strings.HasPrefix(mp, "/mnt/") || len(mp) <= len("/mnt/") {
		return fmt.Errorf("掛載點 %q 必須位於 /mnt/ 底下(例如 /mnt/disk1)", mp)
	}
	if textcheck.HasControl(mp) || strings.ContainsAny(mp, " \t") {
		return fmt.Errorf("掛載點 %q 不可含空白或控制字元", mp)
	}
	if strings.Contains(mp, "..") {
		return fmt.Errorf("掛載點 %q 不可含 '..'", mp)
	}
	return nil
}

// inspectDevice 用 lsblk 看一顆裝置:回傳它是不是「整碟」(top-level
// type==disk),以及它本身與其所有分割區目前的掛載點(空字串不算)。
func inspectDevice(ctx context.Context, r Runner, device string) (isDisk bool, mountpoints []string, err error) {
	out, err := r.Run(ctx, "lsblk", "-J", "-o", "PATH,TYPE,MOUNTPOINT", device)
	if err != nil {
		return false, nil, fmt.Errorf("檢查 %s 失敗: %w", device, err)
	}
	var parsed lsblkOutput
	if err := json.Unmarshal(out, &parsed); err != nil {
		return false, nil, fmt.Errorf("解析 %s 的 lsblk 輸出失敗: %w", device, err)
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
	return os.WriteFile(fstabPath, []byte(body), 0o644)
}
