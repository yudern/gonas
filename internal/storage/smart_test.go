package storage

import (
	"context"
	"errors"
	"testing"
)

const sampleSmartctlOutput = `smartctl 7.3 2022-02-28 r5338 [x86_64-linux-6.1.0] (local build)
Copyright (C) 2002-22, Bruce Allen, Christian Franke, www.smartmontools.org

=== START OF INFORMATION SECTION ===
Model Family:     Western Digital Red
Device Model:     WDC WD40EFAX-68JH4N1
Serial Number:    WD-ABC123

=== START OF READ SMART DATA SECTION ===
SMART overall-health self-assessment test result: PASSED

ID# ATTRIBUTE_NAME          FLAG     VALUE WORST THRESH TYPE      UPDATED  WHEN_FAILED RAW_VALUE
  9 Power_On_Hours          0x0032   098   098   000    Old_age   Always       -       9421
194 Temperature_Celsius     0x0022   116   105   000    Old_age   Always       -       31
`

const sampleSmartctlFailedOutput = `SMART overall-health self-assessment test result: FAILED!`

func TestParseSmartOutput_Passed(t *testing.T) {
	h := parseSmartOutput([]byte(sampleSmartctlOutput))

	if !h.Passed {
		t.Error("expected Passed=true for a PASSED result")
	}
	if h.TempCelsius == nil {
		t.Fatal("expected TempCelsius to be parsed, got nil")
	}
	if *h.TempCelsius != 31 {
		t.Errorf("expected temp 31, got %d", *h.TempCelsius)
	}
}

func TestParseSmartOutput_Failed(t *testing.T) {
	h := parseSmartOutput([]byte(sampleSmartctlFailedOutput))
	if h.Passed {
		t.Error("expected Passed=false for a FAILED result")
	}
}

func TestParseSmartOutput_NoTempAttribute(t *testing.T) {
	h := parseSmartOutput([]byte("SMART overall-health self-assessment test result: PASSED\n"))
	if h.TempCelsius != nil {
		t.Errorf("expected nil TempCelsius when attribute table absent, got %v", *h.TempCelsius)
	}
}

// smartFakeRunner 同時回傳「輸出」跟「錯誤」——模擬 smartctl 用位元遮罩結束碼
// (例如整體健康 FAILING 時以 exit 8 退出)非零退出、但完整報告仍印在 stdout
// 的真實行為(cmd.Output() 會把 stdout 一起帶回來)。共用的 fakeRunner 在
// 設了 err 時只回 nil 輸出,測不到這個情境,所以另開一個。
type smartFakeRunner struct {
	out []byte
	err error
}

func (f smartFakeRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return f.out, f.err
}
func (f smartFakeRunner) RunWithStdin(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
	return f.out, f.err
}

// 第五十八輪全鏈路覆核(QA2)的回歸測試:一顆 SMART 判定 FAILED 的碟,
// smartctl 會以非零結束碼退出、但報告在 stdout。CheckSmartHealth 必須照常
// 解析出 Passed=false,而不是把非零結束碼當成「查詢失敗」丟掉報告——否則
// 告警永遠不會對正在故障的那顆碟觸發。
func TestCheckSmartHealth_FailedDiskReportedViaNonZeroExit(t *testing.T) {
	r := smartFakeRunner{out: []byte(sampleSmartctlFailedOutput), err: errors.New("smartctl [/dev/sda]: exit status 8")}
	h, err := CheckSmartHealth(context.Background(), r, "/dev/sda")
	if err != nil {
		t.Fatalf("expected nil error when a parseable report is present despite non-zero exit, got %v", err)
	}
	if h.Passed {
		t.Error("expected Passed=false for a disk smartctl reported as FAILED via a non-zero exit code")
	}
}

// 對照:真的「跑不起來」(找不到執行檔/開不了裝置),輸出裡沒有健康摘要,
// 這時候才該當成查詢失敗回錯。
func TestCheckSmartHealth_LaunchFailureReturnsError(t *testing.T) {
	r := smartFakeRunner{out: nil, err: errors.New("exec: \"smartctl\": executable file not found in $PATH")}
	if _, err := CheckSmartHealth(context.Background(), r, "/dev/sda"); err == nil {
		t.Error("expected an error when smartctl could not run at all (no parseable report in output)")
	}
}
