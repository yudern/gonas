package filemanager

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveStream_WritesFile(t *testing.T) {
	root := setupRoot(t)

	entry, err := SaveStream(root, "", "hello.txt", strings.NewReader("hello upload"))
	if err != nil {
		t.Fatalf("SaveStream returned error: %v", err)
	}
	if entry.Name != "hello.txt" || entry.Size != int64(len("hello upload")) {
		t.Errorf("unexpected entry: %+v", entry)
	}
	data, err := os.ReadFile(filepath.Join(root, "hello.txt"))
	if err != nil || string(data) != "hello upload" {
		t.Fatalf("got %q, err %v", data, err)
	}
}

func TestSaveStream_IntoSubdirectory(t *testing.T) {
	root := setupRoot(t)
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := SaveStream(root, "sub", "f.txt", strings.NewReader("x")); err != nil {
		t.Fatalf("SaveStream returned error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "sub", "f.txt")); err != nil {
		t.Errorf("expected file in subdirectory: %v", err)
	}
}

func TestSaveStream_OverwritesExisting(t *testing.T) {
	root := setupRoot(t)
	writeFile(t, filepath.Join(root, "f.txt"), "old")

	if _, err := SaveStream(root, "", "f.txt", strings.NewReader("new")); err != nil {
		t.Fatalf("SaveStream returned error: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "f.txt"))
	if err != nil || string(data) != "new" {
		t.Fatalf("expected upload to overwrite existing file, got %q, err %v", data, err)
	}
}

func TestSaveStream_RejectsFilenameWithSlash(t *testing.T) {
	root := setupRoot(t)
	if _, err := SaveStream(root, "", "sneaky/../escape.txt", strings.NewReader("x")); !errors.Is(err, ErrInvalidName) {
		t.Errorf("got %v, want ErrInvalidName", err)
	}
}

func TestSaveStream_RejectsMissingDestinationDirectory(t *testing.T) {
	root := setupRoot(t)
	if _, err := SaveStream(root, "does-not-exist", "f.txt", strings.NewReader("x")); err != ErrNotFound {
		t.Errorf("got %v, want ErrNotFound", err)
	}
}

func TestSaveStream_NoLeftoverTempFileOnSuccess(t *testing.T) {
	root := setupRoot(t)
	if _, err := SaveStream(root, "", "f.txt", strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "f.txt" {
		t.Errorf("expected only f.txt to remain, got %+v", entries)
	}
}
