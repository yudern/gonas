// Package cmdrunner 是「呼叫外部系統指令」這件事在 GoNAS 裡唯一的實作,
// 被 internal/storage(lsblk/smartctl/mergerfs/snapraid)與 internal/share
// (useradd/smbpasswd/exportfs……)共用。
//
// 抽成獨立套件而不是各自重複實作,單純是因為這兩層都需要同一件事：一個
// 可以在單元測試裡用假物件替換掉的「執行指令」介面，這樣測試能斷言
// 「組出來的指令對不對」而不必真的動系統(不需要 root、不需要真的硬碟,
// 也不需要真的建立/刪除系統帳號)。
package cmdrunner

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
)

// Runner 執行一個外部指令。分成 Run(無標準輸入)與 RunWithStdin(需要餵資料
// 進去，例如 chpasswd / smbpasswd 這種「密碼從 stdin 讀」的工具)兩個方法,
// 而不是統一用一個带 io.Reader 參數的簽名，是為了讓多數呼叫端(不需要 stdin
// 的絕大多數指令)寫起來更簡潔。
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
	RunWithStdin(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error)
}

// execRunner 是正式環境使用的 Runner，直接透過 os/exec 呼叫系統指令。
type execRunner struct{}

// NewExecRunner 建立一個會真的執行系統指令的 Runner。
func NewExecRunner() Runner { return execRunner{} }

func (execRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return runCmd(ctx, nil, name, args...)
}

func (execRunner) RunWithStdin(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
	return runCmd(ctx, stdin, name, args...)
}

func runCmd(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return out, fmt.Errorf("%s %v: %w (stderr: %s)", name, args, err, string(exitErr.Stderr))
		}
		return out, fmt.Errorf("%s %v: %w", name, args, err)
	}
	return out, nil
}
