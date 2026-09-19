// Package doctor 檢查 GoNAS 依賴的選用外部指令是不是已經裝在這台機器上。
//
// GoNAS 本身(gonasd 這個執行檔)是零第三方依賴的單一靜態執行檔,裝上去
// 就能開機、就能看到 Web UI —— 但陣列(mergerFS/SnapRAID)、檔案分享
// (Samba/NFS)、VPN(WireGuard)、備份(rsync)、SMART 健康檢查這些功能,
// 全部要靠對應的系統指令實際存在才會動,GoNAS 自己不負責幫使用者裝
// 這些套件(見 internal/share、internal/storage、internal/wireguard、
// internal/backup 各自套件開頭「已知取捨」的說明,這台開發沙盒的網路
// 白名單也擋掉了套件源,所以連 CI 這裡都沒辦法真的裝上去測試)。
//
// 這支套件存在的理由:讓使用者在安裝完 gonasd 之後,能立刻知道「我這台
// 機器還缺什麼,裝了才能用哪個功能」,而不是要等到真的點下去某個功能
// 才在 Web UI 上看到一則「套用失敗」的警告才發現。install.sh 安裝完會
// 自動跑一次這個檢查並印出報告(見 cmd/gonasd 的 -check-deps 旗標)。
package doctor

import (
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// Check 描述一個外部指令依賴:這個指令是給哪個 GoNAS 功能用的。目前
// GoNAS 完全沒有「沒裝就無法啟動 gonasd 本身」的必要外部依賴 —— 陣列/
// 分享/VPN/備份/SMART 這些全部是「沒裝就那個功能不能用,其他功能不受
// 影響」的軟性依賴,所以這裡不需要 Required 欄位區分必要/選用,列表裡
// 的每一項本質上都是選用的。
type Check struct {
	Command string // 要用 exec.LookPath 找的指令名稱
	Feature string // 這個指令支援 GoNAS 的哪個功能,給報告顯示用
}

// Checks 是 GoNAS 目前所有功能會用到的外部指令清單,跟各套件實際呼叫的
// 指令名稱保持一致(見 internal/storage、internal/share、
// internal/wireguard、internal/backup 裡透過 cmdrunner.Runner 呼叫的
// 指令)。新增一個會呼叫外部指令的功能時,這裡也要跟著補一筆,不然
// 使用者在這份報告上看不到那個功能其實還缺依賴。
var Checks = []Check{
	{Command: "lsblk", Feature: "硬碟偵測"},
	{Command: "smartctl", Feature: "SMART 健康檢查"},
	{Command: "mergerfs", Feature: "儲存池掛載(mergerFS)"},
	{Command: "snapraid", Feature: "同位校驗(SnapRAID)"},
	{Command: "useradd", Feature: "建立系統使用者帳號"},
	{Command: "userdel", Feature: "刪除系統使用者帳號"},
	{Command: "chpasswd", Feature: "設定系統使用者密碼"},
	{Command: "groupadd", Feature: "建立系統群組"},
	{Command: "smbd", Feature: "SMB 檔案分享(Samba 主程式)"},
	{Command: "testparm", Feature: "SMB 設定驗證"},
	{Command: "smbcontrol", Feature: "SMB 設定熱重載"},
	{Command: "smbpasswd", Feature: "SMB 密碼同步"},
	{Command: "exportfs", Feature: "NFS 檔案分享"},
	{Command: "wg", Feature: "WireGuard VPN 狀態查詢"},
	{Command: "wg-quick", Feature: "WireGuard VPN 啟停"},
	{Command: "rsync", Feature: "備份(硬連結輪替快照)"},
}

// --- 套件層級(給 Web「系統診斷 / Doctor」頁面 + 一鍵安裝用)-----------
//
// Checks 是「指令」層級(給 CLI -check-deps 的文字報告),但一鍵安裝要的是
// 「apt 套件」層級:一個套件(samba)通常提供多個指令(smbd/testparm/…)。
// OptionalPackages 把 GoNAS 的選用外部相依整理成「可安裝的套件」單位,每個
// 套件全部指令都找得到才算裝好。刻意只收「選用、且真的能用 apt 一鍵補裝」
// 的套件 —— useradd/lsblk 這種屬於系統基礎套件(passwd/util-linux)、幾乎
// 一定已存在,不放進一鍵安裝清單。
//
// Key 是穩定識別碼,給前端 i18n 對照顯示名稱/說明用(後端不回中文字串,
// 維持三語介面各自在地化)。Apt 是 `apt-get install` 的套件名,同時也是
// 一鍵安裝的「白名單」—— 只有出現在這份清單裡的套件名才准安裝,避免
// 「一鍵安裝」變成任意 apt install 注入。
type OptionalPackage struct {
	Key      string   `json:"key"`
	Apt      string   `json:"apt"`
	Commands []string `json:"commands"`
}

// OptionalPackages 的每個指令都必須跟 Checks 裡列的一致(同一份事實)。
var OptionalPackages = []OptionalPackage{
	{Key: "smartmontools", Apt: "smartmontools", Commands: []string{"smartctl"}},
	{Key: "mergerfs", Apt: "mergerfs", Commands: []string{"mergerfs"}},
	{Key: "snapraid", Apt: "snapraid", Commands: []string{"snapraid"}},
	{Key: "samba", Apt: "samba", Commands: []string{"smbd", "testparm", "smbcontrol", "smbpasswd"}},
	{Key: "nfs", Apt: "nfs-kernel-server", Commands: []string{"exportfs"}},
	{Key: "wireguard", Apt: "wireguard-tools", Commands: []string{"wg", "wg-quick"}},
	{Key: "rsync", Apt: "rsync", Commands: []string{"rsync"}},
	{Key: "docker", Apt: "docker.io", Commands: []string{"docker"}},
}

// PackageStatus 是一個 OptionalPackage 檢查後的狀態。
type PackageStatus struct {
	OptionalPackage
	Installed bool     `json:"installed"`
	Missing   []string `json:"missing"` // 缺哪些指令(Installed 為 false 時才有意義)
}

// RunPackages 逐一檢查 OptionalPackages 裡每個套件的指令是否都存在。
func RunPackages() []PackageStatus {
	out := make([]PackageStatus, 0, len(OptionalPackages))
	for _, p := range OptionalPackages {
		var missing []string
		for _, cmd := range p.Commands {
			if _, err := exec.LookPath(cmd); err != nil {
				missing = append(missing, cmd)
			}
		}
		out = append(out, PackageStatus{
			OptionalPackage: p,
			Installed:       len(missing) == 0,
			Missing:         missing,
		})
	}
	return out
}

// IsInstallable 回報 apt 是不是 OptionalPackages 白名單裡的套件名 —— 一鍵
// 安裝只准安裝這份清單裡的套件,擋掉把任意套件名(甚至帶參數的字串)送進
// apt-get 的可能。
func IsInstallable(apt string) bool {
	for _, p := range OptionalPackages {
		if p.Apt == apt {
			return true
		}
	}
	return false
}

// Result 是一個 Check 實際檢查後的結果。
type Result struct {
	Check
	Found bool
	Path  string // Found 為 true 時,指令實際被找到的完整路徑
}

// Run 對 Checks 清單逐一呼叫 exec.LookPath。這裡刻意不去真的執行任何
// 指令(例如 `docker version` 或 `smbd --version`)—— 找不找得到指令跟
// 這個指令能不能正常運作是兩件事,後者留給使用者實際使用那個功能時
// 由各自的 handler 處理(例如 Docker daemon 沒啟動時 /api/v1/docker/ping
// 會回報清楚的錯誤),Run 只回答最基本的「這台機器上裝了沒有」。
func Run() []Result {
	results := make([]Result, 0, len(Checks))
	for _, c := range Checks {
		path, err := exec.LookPath(c.Command)
		results = append(results, Result{
			Check: c,
			Found: err == nil,
			Path:  path,
		})
	}
	return results
}

// Report 把 Run() 的結果格式化成人類可讀的報告,寫到 w。獨立成一個函式
// (而不是直接在 cmd/gonasd 裡拼字串)方便寫測試斷言格式,也方便之後
// 如果要在 Web UI 加一個「系統診斷」頁面時直接重用同一份邏輯,不用
// 重新格式化一次。
func Report(w io.Writer, results []Result) {
	fmt.Fprintln(w, "GoNAS 選用外部依賴檢查:")
	fmt.Fprintln(w, "(以下全部是選用依賴 —— 沒裝只代表對應的功能沒辦法用,gonasd 本身照常啟動)")
	fmt.Fprintln(w)

	nameWidth := 0
	for _, r := range results {
		if len(r.Command) > nameWidth {
			nameWidth = len(r.Command)
		}
	}

	var missing []Result
	for _, r := range results {
		status := "✔ 已安裝"
		detail := r.Path
		if !r.Found {
			status = "✘ 未安裝"
			detail = ""
			missing = append(missing, r)
		}
		fmt.Fprintf(w, "  %-*s  %-10s %-28s %s\n", nameWidth, r.Command, status, r.Feature, detail)
	}

	fmt.Fprintln(w)
	if len(missing) == 0 {
		fmt.Fprintln(w, "全部依賴都已安裝。")
		return
	}

	names := make([]string, len(missing))
	for i, r := range missing {
		names[i] = r.Command
	}
	fmt.Fprintf(w, "缺少 %d 項:%s —— 對應功能在 Web UI 上會顯示「設定已存、套用失敗」這類優雅降級的警告,不影響其他功能。\n", len(missing), strings.Join(names, ", "))
}
