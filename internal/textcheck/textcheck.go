// Package textcheck 提供產生「一行一個指令」的設定檔時共用的欄位安全檢查。
//
// GoNAS 有好幾個設定檔是用 text/template 產生的:smb.conf、/etc/exports、
// WireGuard 的 wg .conf、SnapRAID 的 snapraid.conf。這些格式都是「一行一個
// 設定」,而 text/template **不會跳脫換行**——所以任何寫進去的欄位只要含有
// 換行或其他控制字元,就可能破壞檔案格式、甚至在使用者以為只是填一個名字/
// 路徑時,注入一整條額外的設定指令。這些設定端點目前都是 requireAdmin,
// 威脅有限,但這是應該擋的縱深防禦 + 正確性問題,所以把「欄位不得含控制
// 字元」這個判斷收斂成一個地方,讓各個設定產生器共用同一套檢查。
package textcheck

import "unicode"

// HasControl 回報 s 是否含有換行、CR、tab 或其他 C0/C1 控制字元。
func HasControl(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}
