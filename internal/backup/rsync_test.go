package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeRunner 記錄每一次 Run 呼叫的指令與參數,並在成功時把來源目錄的內容
// 「複製」進目的地(用最陽春的方式模擬 rsync 真正會做的事,足夠讓
// RunBackup 之後的 promote/prune 邏輯有真實檔案可以操作),讓測試不需要
// 這台機器真的裝 rsync ——這台開發沙盒沒有裝(見套件開頭註解)。
type fakeRunner struct {
	calls   []call
	failErr error
}

type call struct {
	name string
	args []string
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, call{name: name, args: args})
	if f.failErr != nil {
		return nil, f.failErr
	}
	if name == "rsync" && len(args) >= 2 {
		src := args[len(args)-2]
		dst := args[len(args)-1]
		if err := os.MkdirAll(dst, 0o755); err != nil {
			return nil, err
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			data, err := os.ReadFile(filepath.Join(src, e.Name()))
			if err != nil {
				continue
			}
			if err := os.WriteFile(filepath.Join(dst, e.Name()), data, 0o644); err != nil {
				return nil, err
			}
		}
	}
	return nil, nil
}

func (f *fakeRunner) RunWithStdin(ctx context.Context, _ []byte, name string, args ...string) ([]byte, error) {
	return f.Run(ctx, name, args...)
}

func TestRunBackup_Success_PromotesSnapshotAndRunsRsyncWithExpectedArgs(t *testing.T) {
	job := testJob(t, 5)
	if err := os.WriteFile(filepath.Join(job.SourcePath, "a.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := &fakeRunner{}
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

	result := RunBackup(context.Background(), r, job, now)

	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}
	if result.SnapshotDir == "" {
		t.Fatal("expected a non-empty snapshot directory in the result")
	}
	if _, err := os.Stat(filepath.Join(result.SnapshotDir, "a.txt")); err != nil {
		t.Errorf("expected the snapshot to contain the backed-up file: %v", err)
	}

	if len(r.calls) != 1 || r.calls[0].name != "rsync" {
		t.Fatalf("expected exactly one rsync call, got %+v", r.calls)
	}
	args := r.calls[0].args
	if args[0] != "-aAX" || args[1] != "--delete" {
		t.Errorf("expected -aAX --delete as the first two args, got %v", args)
	}
	// 第一次執行還沒有任何既有快照,不該出現 --link-dest。
	for _, a := range args {
		if len(a) >= len("--link-dest=") && a[:len("--link-dest=")] == "--link-dest=" {
			t.Errorf("expected no --link-dest on the very first run, got arg %q", a)
		}
	}
	lastArg := args[len(args)-1]
	if lastArg != result.SnapshotDir+partialSuffix {
		// rsync 執行當下目的地還是 .partial,promote 是之後才發生的重新命名。
		t.Errorf("expected rsync destination arg to be the .partial dir, got %q (snapshot promoted to %q)", lastArg, result.SnapshotDir)
	}
	srcArg := args[len(args)-2]
	if srcArg != job.SourcePath+"/" {
		t.Errorf("expected source arg to have a trailing slash, got %q", srcArg)
	}
}

func TestRunBackup_SecondRun_IncludesLinkDestFromFirstSnapshot(t *testing.T) {
	job := testJob(t, 5)
	if err := os.WriteFile(filepath.Join(job.SourcePath, "a.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := &fakeRunner{}

	first := RunBackup(context.Background(), r, job, time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC))
	if !first.Success {
		t.Fatalf("first run failed: %s", first.Error)
	}

	second := RunBackup(context.Background(), r, job, time.Date(2026, 3, 2, 12, 0, 0, 0, time.UTC))
	if !second.Success {
		t.Fatalf("second run failed: %s", second.Error)
	}

	if len(r.calls) != 2 {
		t.Fatalf("expected 2 rsync calls, got %d", len(r.calls))
	}
	found := false
	for _, a := range r.calls[1].args {
		if a == "--link-dest="+first.SnapshotDir {
			found = true
		}
	}
	if !found {
		t.Errorf("expected second run's args to include --link-dest=%s, got %v", first.SnapshotDir, r.calls[1].args)
	}
}

func TestRunBackup_RsyncFailure_CleansUpPartialDirAndReturnsError(t *testing.T) {
	job := testJob(t, 5)
	r := &fakeRunner{failErr: errors.New("rsync: connection unexpectedly closed")}

	result := RunBackup(context.Background(), r, job, time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC))

	if result.Success {
		t.Fatal("expected failure when rsync itself fails")
	}
	if result.Error == "" {
		t.Error("expected a non-empty error message")
	}

	entries, err := os.ReadDir(jobDir(job))
	if err == nil {
		for _, e := range entries {
			if filepath.Ext(e.Name()) == partialSuffix {
				t.Errorf("expected the failed .partial directory to be cleaned up, found %q", e.Name())
			}
		}
	}
}

func TestRunBackup_InvalidJob_FailsWithoutCallingRunner(t *testing.T) {
	job := testJob(t, 5)
	job.Name = "" // 讓 Validate 失敗
	r := &fakeRunner{}

	result := RunBackup(context.Background(), r, job, time.Now())

	if result.Success {
		t.Fatal("expected an invalid job to fail validation")
	}
	if len(r.calls) != 0 {
		t.Errorf("expected RunBackup to never call the runner for an invalid job, got %d calls", len(r.calls))
	}
}

func TestRunBackup_PruningRespectsRetentionAcrossManyRuns(t *testing.T) {
	job := testJob(t, 2)
	if err := os.WriteFile(filepath.Join(job.SourcePath, "a.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := &fakeRunner{}

	base := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		result := RunBackup(context.Background(), r, job, base.AddDate(0, 0, i))
		if !result.Success {
			t.Fatalf("run %d failed: %s", i, result.Error)
		}
	}

	names, err := listSnapshots(job)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != job.RetentionCount {
		t.Errorf("expected exactly %d snapshots retained, got %d: %v", job.RetentionCount, len(names), names)
	}
}
