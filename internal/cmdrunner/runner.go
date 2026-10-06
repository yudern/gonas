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
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
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

// StreamRunner 是選用的擴充介面:邊執行邊把輸出(stdout+stderr 合併)一行
// 一行回報給 onLine,結束時仍回傳完整輸出。給「要即時顯示進度」的長指令
// (例如 apt 安裝)用。呼叫端用型別斷言判斷 Runner 有沒有實作,沒有就退回 Run。
type StreamRunner interface {
	RunStream(ctx context.Context, onLine func(line string), name string, args ...string) ([]byte, error)
}

// RunStream 實作 StreamRunner。\r 也視為換行(apt/dpkg 的進度會用 \r 覆寫同一行)。
func (execRunner) RunStream(ctx context.Context, onLine func(line string), name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw
	var all bytes.Buffer
	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		sc.Split(scanLinesCR)
		for sc.Scan() {
			line := sc.Text()
			all.WriteString(line)
			all.WriteByte('\n')
			if onLine != nil && strings.TrimSpace(line) != "" {
				onLine(line)
			}
		}
		_, _ = io.Copy(io.Discard, pr)
	}()
	err := cmd.Start()
	if err != nil {
		pw.Close()
		<-done
		return all.Bytes(), fmt.Errorf("%s %v: %w", name, args, err)
	}
	err = cmd.Wait()
	pw.Close()
	<-done
	if err != nil {
		tail := all.String()
		if len(tail) > 2000 {
			tail = tail[len(tail)-2000:]
		}
		return all.Bytes(), fmt.Errorf("%s %v: %w (stderr: %s)", name, args, err, tail)
	}
	return all.Bytes(), nil
}

// scanLinesCR 跟 bufio.ScanLines 一樣,但 \r 也當作行尾。
func scanLinesCR(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	if i := bytes.IndexAny(data, "\r\n"); i >= 0 {
		return i + 1, data[:i], nil
	}
	if atEOF {
		return len(data), data, nil
	}
	return 0, nil, nil
}
