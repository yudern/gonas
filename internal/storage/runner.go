package storage

import "github.com/bng147/gonas/internal/cmdrunner"

// Runner 是 storage 套件對外呼叫外部指令(lsblk / smartctl / mergerfs /
// snapraid)的介面。實際定義移到 internal/cmdrunner,讓 internal/share
// 也能共用同一套「可測試的指令執行」機制；這裡用型別別名保留舊名稱,
// 避免動到套件裡其他已經寫好、也已經測試過的程式碼。
type Runner = cmdrunner.Runner

// NewExecRunner 建立一個會真的執行系統指令的 Runner。
func NewExecRunner() Runner { return cmdrunner.NewExecRunner() }
