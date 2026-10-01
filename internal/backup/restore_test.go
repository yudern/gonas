package backup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRunRestore_CopiesSnapshotToTarget(t *testing.T) {
	job := testJob(t, 5)
	if err := os.WriteFile(filepath.Join(job.SourcePath, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := &fakeRunner{}
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	backupRes := RunBackup(context.Background(), r, job, now)
	if !backupRes.Success {
		t.Fatalf("backup failed: %s", backupRes.Error)
	}
	snapName := filepath.Base(backupRes.SnapshotDir)

	target := t.TempDir()
	res := RunRestore(context.Background(), r, job, snapName, target)
	if !res.Success {
		t.Fatalf("restore failed: %s", res.Error)
	}
	if _, err := os.Stat(filepath.Join(target, "a.txt")); err != nil {
		t.Errorf("expected the restored file at the target: %v", err)
	}
}

func TestRunRestore_RejectsUnknownSnapshot(t *testing.T) {
	job := testJob(t, 5)
	r := &fakeRunner{}
	res := RunRestore(context.Background(), r, job, "20990101-000000", t.TempDir())
	if res.Success {
		t.Fatal("expected restore of a non-existent snapshot to fail")
	}
	// 不該真的跑 rsync。
	for _, c := range r.calls {
		if c.name == "rsync" {
			t.Errorf("must not invoke rsync for an unknown snapshot; calls=%+v", r.calls)
		}
	}
}

func TestRunRestore_RejectsRelativeTarget(t *testing.T) {
	job := testJob(t, 5)
	r := &fakeRunner{}
	if res := RunRestore(context.Background(), r, job, "x", "relative/path"); res.Success {
		t.Error("expected a relative target path to be rejected")
	}
}

func TestRunRestore_RejectsTargetInsideDest(t *testing.T) {
	job := testJob(t, 5)
	r := &fakeRunner{}
	inside := filepath.Join(job.DestPath, "sub")
	if res := RunRestore(context.Background(), r, job, "x", inside); res.Success {
		t.Error("expected a target inside the backup destination to be rejected")
	}
}

// 第六十輪安全複審:不得還原到系統關鍵目錄(防以 root 覆寫系統檔)。
func TestRunRestore_RejectsSystemPaths(t *testing.T) {
	job := testJob(t, 5)
	r := &fakeRunner{}
	for _, p := range []string{"/", "/etc", "/etc/cron.d", "/root/.ssh", "/boot", "/usr/bin", "/etc/../etc"} {
		res := RunRestore(context.Background(), r, job, "x", p)
		if res.Success {
			t.Errorf("expected restore into system path %q to be rejected", p)
		}
	}
	// 不該真的跑 rsync。
	for _, c := range r.calls {
		if c.name == "rsync" {
			t.Errorf("must not invoke rsync for a rejected system path; calls=%+v", r.calls)
		}
	}
}

func TestIsSystemPath(t *testing.T) {
	bad := []string{"/", "/etc", "/etc/x", "/root", "/boot/grub", "/usr", "/var/lib", "/etc/../etc"}
	for _, p := range bad {
		if !isSystemPath(p) {
			t.Errorf("expected %q to be a system path", p)
		}
	}
	ok := []string{"/mnt/tank/restore", "/mnt/tank", "/home/user/data", "/srv/share"}
	for _, p := range ok {
		if isSystemPath(p) {
			t.Errorf("expected %q to NOT be a system path", p)
		}
	}
}
