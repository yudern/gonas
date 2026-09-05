package filemanager

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadTextFile_ReturnsContent(t *testing.T) {
	root := setupRoot(t)
	writeFile(t, filepath.Join(root, "notes.txt"), "hello world")

	content, err := ReadTextFile(root, "notes.txt")
	if err != nil {
		t.Fatalf("ReadTextFile returned error: %v", err)
	}
	if content != "hello world" {
		t.Errorf("got %q", content)
	}
}

func TestReadTextFile_RejectsDirectory(t *testing.T) {
	root := setupRoot(t)
	if err := os.Mkdir(filepath.Join(root, "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadTextFile(root, "d"); err != ErrIsADirectory {
		t.Errorf("got %v, want ErrIsADirectory", err)
	}
}

func TestReadTextFile_RejectsTooLarge(t *testing.T) {
	root := setupRoot(t)
	big := strings.Repeat("x", maxTextFileBytes+1)
	writeFile(t, filepath.Join(root, "big.txt"), big)

	if _, err := ReadTextFile(root, "big.txt"); err != ErrFileTooLargeForTextEdit {
		t.Errorf("got %v, want ErrFileTooLargeForTextEdit", err)
	}
}

func TestReadTextFile_RejectsBinaryContent(t *testing.T) {
	root := setupRoot(t)
	if err := os.WriteFile(filepath.Join(root, "bin.dat"), []byte{0xff, 0xfe, 0x00, 0x01, 0x02}, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadTextFile(root, "bin.dat"); err != ErrNotValidUTF8Text {
		t.Errorf("got %v, want ErrNotValidUTF8Text", err)
	}
}

func TestWriteTextFile_CreatesAndOverwrites(t *testing.T) {
	root := setupRoot(t)

	if err := WriteTextFile(root, "notes.txt", "version 1"); err != nil {
		t.Fatalf("WriteTextFile returned error: %v", err)
	}
	content, err := ReadTextFile(root, "notes.txt")
	if err != nil || content != "version 1" {
		t.Fatalf("got %q, err %v", content, err)
	}

	if err := WriteTextFile(root, "notes.txt", "version 2"); err != nil {
		t.Fatalf("WriteTextFile (overwrite) returned error: %v", err)
	}
	content, err = ReadTextFile(root, "notes.txt")
	if err != nil || content != "version 2" {
		t.Fatalf("expected overwritten content, got %q, err %v", content, err)
	}
}

func TestWriteTextFile_RejectsDirectory(t *testing.T) {
	root := setupRoot(t)
	if err := os.Mkdir(filepath.Join(root, "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteTextFile(root, "d", "content"); err != ErrIsADirectory {
		t.Errorf("got %v, want ErrIsADirectory", err)
	}
}

func TestWriteTextFile_RejectsTooLarge(t *testing.T) {
	root := setupRoot(t)
	big := strings.Repeat("x", maxTextFileBytes+1)
	if err := WriteTextFile(root, "big.txt", big); err != ErrFileTooLargeForTextEdit {
		t.Errorf("got %v, want ErrFileTooLargeForTextEdit", err)
	}
}

func TestWriteTextFile_DoesNotLeaveTempFilesBehindOnSuccess(t *testing.T) {
	root := setupRoot(t)
	if err := WriteTextFile(root, "notes.txt", "content"); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "notes.txt" {
		t.Errorf("expected only notes.txt to remain, got %+v", entries)
	}
}
