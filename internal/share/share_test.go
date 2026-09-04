package share

import (
	"context"
	"errors"
)

// fakeRunner 是 internal/share 測試共用的假 Runner，記錄呼叫過的指令與收到的
// stdin,讓測試可以斷言「組出來的指令列/餵進去的資料對不對」而不需要真的
// useradd/smbpasswd/exportfs 之類的系統工具。
type fakeRunner struct {
	output map[string][]byte
	err    map[string]error
	calls  []call
}

type call struct {
	name  string
	args  []string
	stdin []byte
}

var errBoom = errors.New("boom")

func (f *fakeRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return f.RunWithStdin(ctx, nil, name, args...)
}

func (f *fakeRunner) RunWithStdin(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, call{name: name, args: args, stdin: stdin})
	if err, ok := f.err[name]; ok {
		return nil, err
	}
	return f.output[name], nil
}

func (f *fakeRunner) lastCall() call {
	if len(f.calls) == 0 {
		return call{}
	}
	return f.calls[len(f.calls)-1]
}
