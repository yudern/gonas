package backup

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testJob(t *testing.T, retention int) Job {
	t.Helper()
	dest := t.TempDir()
	return Job{
		ID:             "job1",
		Name:           "test",
		SourcePath:     t.TempDir(),
		DestPath:       dest,
		RetentionCount: retention,
		Schedule:       Schedule{EveryHours: 24},
	}
}

func TestListSnapshots_NoJobDirYet_ReturnsEmptyNotError(t *testing.T) {
	job := testJob(t, 3)
	names, err := listSnapshots(job)
	if err != nil {
		t.Fatalf("listSnapshots returned error for a job that never ran: %v", err)
	}
	if len(names) != 0 {
		t.Errorf("expected no snapshots, got %v", names)
	}
}

func TestResolveLinkDest_NoLatestYet_ReturnsEmptyNotError(t *testing.T) {
	job := testJob(t, 3)
	if err := os.MkdirAll(jobDir(job), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := resolveLinkDest(job)
	if err != nil {
		t.Fatalf("resolveLinkDest returned error before any snapshot exists: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty link-dest, got %q", got)
	}
}

func TestPromoteSnapshot_RenamesAndUpdatesLatestLink(t *testing.T) {
	job := testJob(t, 3)
	if err := os.MkdirAll(jobDir(job), 0o755); err != nil {
		t.Fatal(err)
	}
	tmpDir := filepath.Join(jobDir(job), "20260101-000000"+partialSuffix)
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "file.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	finalDir, err := promoteSnapshot(job, tmpDir, at)
	if err != nil {
		t.Fatalf("promoteSnapshot returned error: %v", err)
	}

	if _, err := os.Stat(tmpDir); !os.IsNotExist(err) {
		t.Errorf("expected the .partial directory to no longer exist after promotion, stat err = %v", err)
	}
	if _, err := os.Stat(finalDir); err != nil {
		t.Errorf("expected the promoted snapshot directory to exist: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(finalDir, "file.txt"))
	if err != nil || string(content) != "hello" {
		t.Errorf("expected promoted directory to contain the original file content, got %q, err %v", content, err)
	}

	linkDest, err := resolveLinkDest(job)
	if err != nil {
		t.Fatalf("resolveLinkDest returned error: %v", err)
	}
	if linkDest != finalDir {
		t.Errorf("expected latest link to resolve to %q, got %q", finalDir, linkDest)
	}
}

func TestPromoteSnapshot_UpdatesLatestLinkAcrossMultiplePromotions(t *testing.T) {
	job := testJob(t, 10)
	if err := os.MkdirAll(jobDir(job), 0o755); err != nil {
		t.Fatal(err)
	}

	var lastFinal string
	for i, at := range []time.Time{
		time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC),
	} {
		tmpDir := filepath.Join(jobDir(job), snapshotName(at)+partialSuffix)
		if err := os.MkdirAll(tmpDir, 0o755); err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}
		finalDir, err := promoteSnapshot(job, tmpDir, at)
		if err != nil {
			t.Fatalf("iteration %d: promoteSnapshot returned error: %v", i, err)
		}
		lastFinal = finalDir
	}

	linkDest, err := resolveLinkDest(job)
	if err != nil {
		t.Fatalf("resolveLinkDest returned error: %v", err)
	}
	if linkDest != lastFinal {
		t.Errorf("expected latest link to point at the most recently promoted snapshot %q, got %q", lastFinal, linkDest)
	}
}

func TestListSnapshots_SkipsPartialDirsAndLatestLink(t *testing.T) {
	job := testJob(t, 10)
	if err := os.MkdirAll(jobDir(job), 0o755); err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(jobDir(job), "20260101-000000"), 0o755))
	must(os.MkdirAll(filepath.Join(jobDir(job), "20260102-000000"), 0o755))
	must(os.MkdirAll(filepath.Join(jobDir(job), "20260103-000000"+partialSuffix), 0o755))
	must(os.Symlink(filepath.Join(jobDir(job), "20260102-000000"), latestLinkPath(job)))

	names, err := listSnapshots(job)
	if err != nil {
		t.Fatalf("listSnapshots returned error: %v", err)
	}
	want := []string{"20260101-000000", "20260102-000000"}
	if len(names) != len(want) {
		t.Fatalf("listSnapshots() = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Errorf("listSnapshots()[%d] = %q, want %q", i, names[i], want[i])
		}
	}
}

func TestPruneSnapshots_RemovesOldestBeyondRetention(t *testing.T) {
	job := testJob(t, 2)
	if err := os.MkdirAll(jobDir(job), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"20260101-000000", "20260102-000000", "20260103-000000", "20260104-000000"} {
		if err := os.MkdirAll(filepath.Join(jobDir(job), name), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	if err := pruneSnapshots(job); err != nil {
		t.Fatalf("pruneSnapshots returned error: %v", err)
	}

	names, err := listSnapshots(job)
	if err != nil {
		t.Fatalf("listSnapshots returned error: %v", err)
	}
	want := []string{"20260103-000000", "20260104-000000"}
	if len(names) != len(want) {
		t.Fatalf("after prune, snapshots = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Errorf("after prune, snapshots[%d] = %q, want %q", i, names[i], want[i])
		}
	}
}

func TestListSnapshots_Exported_ParsesTimestampsFromNames(t *testing.T) {
	job := testJob(t, 10)
	if err := os.MkdirAll(jobDir(job), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(jobDir(job), "20260301-120000"), 0o755); err != nil {
		t.Fatal(err)
	}

	infos, err := ListSnapshots(job)
	if err != nil {
		t.Fatalf("ListSnapshots returned error: %v", err)
	}
	if len(infos) != 1 {
		t.Fatalf("expected 1 snapshot, got %d", len(infos))
	}
	if infos[0].Name != "20260301-120000" {
		t.Errorf("Name = %q, want %q", infos[0].Name, "20260301-120000")
	}
	want := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	if !infos[0].CreatedAt.Equal(want) {
		t.Errorf("CreatedAt = %v, want %v", infos[0].CreatedAt, want)
	}
}

func TestPruneSnapshots_FewerThanRetention_RemovesNothing(t *testing.T) {
	job := testJob(t, 10)
	if err := os.MkdirAll(jobDir(job), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(jobDir(job), "20260101-000000"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := pruneSnapshots(job); err != nil {
		t.Fatalf("pruneSnapshots returned error: %v", err)
	}
	names, err := listSnapshots(job)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 {
		t.Errorf("expected the single snapshot to survive pruning, got %v", names)
	}
}
