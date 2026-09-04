// Package storage 是 GoNAS 的儲存核心:硬碟探測、SMART 健康檢查、
// mergerFS 聯合掛載、SnapRAID 同位校驗,以及把這些整合起來的「陣列」
// 啟停狀態機。
//
// 這一層大量呼叫外部指令(lsblk / smartctl / mergerfs / snapraid),
// 所以刻意把「怎麼執行指令」抽成 Runner 介面 —— 這樣單元測試可以塞
// 一個假的 Runner 進來驗證邏輯(組出來的指令對不對、狀態機轉得對不對),
// 完全不需要真的硬碟或 root 權限。等有實體機器時,真正的 execRunner
// 會直接呼叫系統上的這些工具。
package storage

import (
	"context"
	"fmt"
	"os/exec"
)

// Runner 執行一個外部指令並回傳標準輸出。之所以不用 *exec.Cmd 直接穿透
// 到每個檔案裡,是因為測試需要能斷言「呼叫了什麼指令」而不用真的執行。
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// execRunner 是正式環境使用的 Runner,直接透過 os/exec 呼叫系統指令。
type execRunner struct{}

// NewExecRunner 建立一個會真的執行系統指令的 Runner。
func NewExecRunner() Runner { return execRunner{} }

func (execRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return out, fmt.Errorf("%s %v: %w (stderr: %s)", name, args, err, string(exitErr.Stderr))
		}
		return out, fmt.Errorf("%s %v: %w", name, args, err)
	}
	return out, nil
}
