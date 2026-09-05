package filemanager

import (
	"archive/zip"
	"bytes"
	"io"
	"path/filepath"
	"sort"
	"testing"
)

func TestWriteZip_ContainsAllFilesUnderFolderPrefix(t *testing.T) {
	root := setupRoot(t)
	writeFile(t, filepath.Join(root, "docs", "a.txt"), "aaa")
	writeFile(t, filepath.Join(root, "docs", "nested", "b.txt"), "bbb")

	var buf bytes.Buffer
	if err := WriteZip(&buf, root, "docs"); err != nil {
		t.Fatalf("WriteZip returned error: %v", err)
	}

	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("reading generated zip: %v", err)
	}

	var names []string
	contents := map[string]string{}
	for _, f := range zr.File {
		names = append(names, f.Name)
		if f.FileInfo().IsDir() {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("opening zip entry %q: %v", f.Name, err)
		}
		data, _ := io.ReadAll(rc)
		rc.Close()
		contents[f.Name] = string(data)
	}
	sort.Strings(names)

	if contents["docs/a.txt"] != "aaa" {
		t.Errorf("expected docs/a.txt content, got entries %+v", names)
	}
	if contents["docs/nested/b.txt"] != "bbb" {
		t.Errorf("expected docs/nested/b.txt content, got entries %+v", names)
	}
}

func TestWriteZip_RejectsFile(t *testing.T) {
	root := setupRoot(t)
	writeFile(t, filepath.Join(root, "a.txt"), "x")

	var buf bytes.Buffer
	if err := WriteZip(&buf, root, "a.txt"); err != ErrNotADirectory {
		t.Errorf("got %v, want ErrNotADirectory", err)
	}
}

func TestWriteZip_ExcludesTrashDirectory(t *testing.T) {
	root := setupRoot(t)
	writeFile(t, filepath.Join(root, "a.txt"), "keep")
	if err := Delete(root, "a.txt", false); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "b.txt"), "also keep")

	var buf bytes.Buffer
	if err := WriteZip(&buf, root, ""); err != nil {
		t.Fatalf("WriteZip returned error: %v", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("reading generated zip: %v", err)
	}
	for _, f := range zr.File {
		if len(f.Name) >= len(trashDirName) && f.Name[:len(trashDirName)] == trashDirName {
			t.Errorf("expected trash directory to be excluded from zip, found %q", f.Name)
		}
	}
}
