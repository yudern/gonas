package share

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestGenerateSambaConfig_RecycleBlock 驗證開了回收筒的共享會帶 vfs_recycle 設定,
// 沒開的不會。
func TestGenerateSambaConfig_RecycleBlock(t *testing.T) {
	shares := []Share{
		{Name: "docs", Path: "/mnt/tank/docs", Recycle: true, RecycleMaxDays: 30},
		{Name: "scratch", Path: "/mnt/tank/scratch"},
	}
	out, err := GenerateSambaConfig(shares, "/etc/samba/gonas-shares.conf")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	docsIdx := strings.Index(out, "[docs]")
	scratchIdx := strings.Index(out, "[scratch]")
	if docsIdx < 0 || scratchIdx < 0 {
		t.Fatalf("both shares should be present:\n%s", out)
	}
	docsSection := out[docsIdx:scratchIdx]
	if !strings.Contains(docsSection, "vfs objects = recycle") {
		t.Errorf("recycle-enabled share should have vfs recycle config:\n%s", docsSection)
	}
	if !strings.Contains(docsSection, "recycle:repository = .recycle/%U") {
		t.Errorf("expected per-user recycle repository:\n%s", docsSection)
	}
	scratchSection := out[scratchIdx:]
	if strings.Contains(scratchSection, "vfs objects = recycle") {
		t.Errorf("share without recycle should not have recycle config:\n%s", scratchSection)
	}
}

func TestShareValidate_RejectsNegativeRecycleDays(t *testing.T) {
	sh := Share{Name: "x", Path: "/mnt/x", Recycle: true, RecycleMaxDays: -1}
	if err := sh.Validate(); err == nil {
		t.Error("expected negative recycle retention to be rejected")
	}
}

// TestGenerateSambaConfig_WriteReadLists 驗證 write list / read list 會分別產生
// 對應的 smb.conf 行,空名單則不產生。
func TestGenerateSambaConfig_WriteReadLists(t *testing.T) {
	shares := []Share{
		{Name: "media", Path: "/mnt/tank/media", ReadOnly: true, WriteList: []string{"alice", "bob"}},
		{Name: "drop", Path: "/mnt/tank/drop", ReadList: []string{"guest"}},
		{Name: "plain", Path: "/mnt/tank/plain"},
	}
	out, err := GenerateSambaConfig(shares, "/etc/samba/gonas-shares.conf")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	mediaIdx := strings.Index(out, "[media]")
	dropIdx := strings.Index(out, "[drop]")
	plainIdx := strings.Index(out, "[plain]")
	mediaSec := out[mediaIdx:dropIdx]
	dropSec := out[dropIdx:plainIdx]
	plainSec := out[plainIdx:]

	if !strings.Contains(mediaSec, "write list = alice, bob") {
		t.Errorf("media section missing write list:\n%s", mediaSec)
	}
	if strings.Contains(mediaSec, "read list =") {
		t.Errorf("media section should not have a read list:\n%s", mediaSec)
	}
	if !strings.Contains(dropSec, "read list = guest") {
		t.Errorf("drop section missing read list:\n%s", dropSec)
	}
	if strings.Contains(plainSec, "write list =") || strings.Contains(plainSec, "read list =") {
		t.Errorf("plain section should have neither list:\n%s", plainSec)
	}
}

func TestShareValidate_RejectsUnsafeWriteListEntry(t *testing.T) {
	// 含逗號的名字會被 smbd 拆成多個使用者(注入),必須擋。
	sh := Share{Name: "x", Path: "/mnt/x", WriteList: []string{"alice,bob"}}
	if err := sh.Validate(); err == nil {
		t.Error("expected a comma in a write-list entry to be rejected")
	}
	// 含空白同理。
	sh2 := Share{Name: "x", Path: "/mnt/x", ReadList: []string{"bad name"}}
	if err := sh2.Validate(); err == nil {
		t.Error("expected a space in a read-list entry to be rejected")
	}
}

func TestPruneRecycleBin_RemovesOldKeepsRecent(t *testing.T) {
	sharePath := t.TempDir()
	recycleDir := filepath.Join(sharePath, RecycleDirName, "alice", "sub")
	if err := os.MkdirAll(recycleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	oldFile := filepath.Join(recycleDir, "old.txt")
	newFile := filepath.Join(recycleDir, "new.txt")
	writeWithMtime(t, oldFile, now.Add(-40*24*time.Hour))
	writeWithMtime(t, newFile, now.Add(-2*24*time.Hour))

	removed, err := PruneRecycleBin(sharePath, 30*24*time.Hour, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if removed != 1 {
		t.Errorf("expected 1 file removed, got %d", removed)
	}
	if _, err := os.Stat(oldFile); !os.IsNotExist(err) {
		t.Error("old file should have been removed")
	}
	if _, err := os.Stat(newFile); err != nil {
		t.Error("recent file should have been kept")
	}
}

func TestPruneRecycleBin_RemovesEmptiedDirsButKeepsRoot(t *testing.T) {
	sharePath := t.TempDir()
	root := filepath.Join(sharePath, RecycleDirName)
	deepDir := filepath.Join(root, "bob", "deep")
	if err := os.MkdirAll(deepDir, 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	writeWithMtime(t, filepath.Join(deepDir, "gone.txt"), now.Add(-100*24*time.Hour))

	if _, err := PruneRecycleBin(sharePath, 7*24*time.Hour, now); err != nil {
		t.Fatal(err)
	}
	// 子目錄因為空了應被移除。
	if _, err := os.Stat(deepDir); !os.IsNotExist(err) {
		t.Error("emptied subdirectory should have been removed")
	}
	// .recycle 根目錄本身要留著。
	if _, err := os.Stat(root); err != nil {
		t.Error(".recycle root should be kept even when empty")
	}
}

func TestPruneRecycleBin_ZeroMaxAgeIsNoOp(t *testing.T) {
	sharePath := t.TempDir()
	recycleDir := filepath.Join(sharePath, RecycleDirName)
	if err := os.MkdirAll(recycleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(recycleDir, "keep.txt")
	writeWithMtime(t, f, time.Now().Add(-1000*24*time.Hour))

	removed, err := PruneRecycleBin(sharePath, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if removed != 0 {
		t.Errorf("maxAge 0 should remove nothing, got %d", removed)
	}
	if _, err := os.Stat(f); err != nil {
		t.Error("file should be kept when retention is 0 (keep forever)")
	}
}

func TestPruneRecycleBin_NoRecycleDir(t *testing.T) {
	removed, err := PruneRecycleBin(t.TempDir(), 30*24*time.Hour, time.Now())
	if err != nil {
		t.Fatalf("a share that never had a recycle bin should not error: %v", err)
	}
	if removed != 0 {
		t.Errorf("expected 0 removed, got %d", removed)
	}
}

func writeWithMtime(t *testing.T, path string, mtime time.Time) {
	t.Helper()
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}
