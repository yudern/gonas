package storage

import (
	"context"
	"errors"
	"testing"
)

const sampleSelfTestLog = `smartctl 7.3 2022-02-28 r5338 [x86_64-linux-6.1.0] (local build)
Copyright (C) 2002-22, Bruce Allen, Christian Franke, www.smartmontools.org

=== START OF READ SMART DATA SECTION ===
SMART Self-test log structure revision number 1
Num  Test_Description    Status                  Remaining  LifeTime(hours)  LBA_of_first_error
# 1  Extended offline    Completed without error       00%      9421         -
# 2  Short offline       Completed without error       00%      9405         -
# 3  Short offline       Completed: read failure       40%      9390         123456789
# 4  Extended offline    Self-test routine in progress 60%      9388         -
`

func TestParseSelfTestLog_ParsesEntries(t *testing.T) {
	entries := parseSelfTestLog([]byte(sampleSelfTestLog))
	if len(entries) != 4 {
		t.Fatalf("expected 4 entries, got %d: %+v", len(entries), entries)
	}

	if entries[0].Num != 1 || entries[0].Description != "Extended offline" || !entries[0].Passed {
		t.Errorf("entry 0 mismatch: %+v", entries[0])
	}
	if entries[0].RemainingPct != 0 || entries[0].LifetimeHours != 9421 {
		t.Errorf("entry 0 numeric fields wrong: %+v", entries[0])
	}
	// 讀取失敗那一列不算通過。
	if entries[2].Passed {
		t.Errorf("entry 2 (read failure) must not be Passed: %+v", entries[2])
	}
	if entries[2].RemainingPct != 40 {
		t.Errorf("entry 2 remaining wrong: %+v", entries[2])
	}
	// 進行中那一列不算通過。
	if entries[3].Passed {
		t.Errorf("entry 3 (in progress) must not be Passed: %+v", entries[3])
	}
	if entries[3].RemainingPct != 60 {
		t.Errorf("entry 3 remaining wrong: %+v", entries[3])
	}
}

func TestParseSelfTestLog_NoEntries(t *testing.T) {
	entries := parseSelfTestLog([]byte("No self-tests have been logged.\n"))
	if len(entries) != 0 {
		t.Errorf("expected no entries for an empty log, got %+v", entries)
	}
}

func TestReadSmartSelfTestLog_NonZeroExitButParsable(t *testing.T) {
	// smartctl 常以非零結束碼退出但仍印出紀錄;只要有可解析的列就照常回傳。
	r := smartFakeRunner{out: []byte(sampleSelfTestLog), err: errors.New("smartctl: exit status 4")}
	entries, err := ReadSmartSelfTestLog(context.Background(), r, "/dev/sda")
	if err != nil {
		t.Fatalf("expected parsable output to override non-zero exit, got err: %v", err)
	}
	if len(entries) != 4 {
		t.Errorf("expected 4 entries, got %d", len(entries))
	}
}

func TestReadSmartSelfTestLog_RealFailureNoOutput(t *testing.T) {
	// 完全沒有可解析的紀錄列 + 非零結束碼 = 真的失敗(例如裝置打不開)。
	r := smartFakeRunner{out: []byte("smartctl: cannot open device"), err: errors.New("exit status 2")}
	if _, err := ReadSmartSelfTestLog(context.Background(), r, "/dev/sdz"); err == nil {
		t.Error("expected an error when there is no parsable log and smartctl failed")
	}
}

func TestStartSmartSelfTest_RejectsUnknownKind(t *testing.T) {
	r := smartFakeRunner{}
	if err := StartSmartSelfTest(context.Background(), r, "/dev/sda", "medium"); err == nil {
		t.Error("expected an error for an unknown self-test kind")
	}
}

func TestStartSmartSelfTest_ShortSucceeds(t *testing.T) {
	r := smartFakeRunner{out: []byte("Testing has begun.")}
	if err := StartSmartSelfTest(context.Background(), r, "/dev/sda", SmartSelfTestShort); err != nil {
		t.Errorf("unexpected error starting a short self-test: %v", err)
	}
}
