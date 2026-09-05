package filemanager

import (
	"path/filepath"
	"testing"
)

func TestSearch_FindsMatchingNamesRecursively(t *testing.T) {
	root := setupRoot(t)
	writeFile(t, filepath.Join(root, "report-2026.txt"), "x")
	writeFile(t, filepath.Join(root, "sub", "old-report.txt"), "x")
	writeFile(t, filepath.Join(root, "sub", "photo.jpg"), "x")

	result, err := Search(root, "", "report")
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if len(result.Entries) != 2 {
		t.Fatalf("expected 2 matches, got %d: %+v", len(result.Entries), result.Entries)
	}
	if result.Truncated {
		t.Error("did not expect truncation for a small result set")
	}
}

func TestSearch_IsCaseInsensitive(t *testing.T) {
	root := setupRoot(t)
	writeFile(t, filepath.Join(root, "IMPORTANT.txt"), "x")

	result, err := Search(root, "", "important")
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if len(result.Entries) != 1 {
		t.Fatalf("expected case-insensitive match, got %+v", result.Entries)
	}
}

func TestSearch_SkipsTrashDirectory(t *testing.T) {
	root := setupRoot(t)
	writeFile(t, filepath.Join(root, "a.txt"), "x")
	if err := Delete(root, "a.txt", false); err != nil {
		t.Fatal(err)
	}
	// 回收桶裡現在有一個叫 "...-a.txt" 的項目，搜尋 "a.txt" 不該找到它。

	result, err := Search(root, "", "a.txt")
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if len(result.Entries) != 0 {
		t.Errorf("expected trash contents to be excluded from search, got %+v", result.Entries)
	}
}

func TestSearch_EmptyQueryReturnsNoResults(t *testing.T) {
	root := setupRoot(t)
	writeFile(t, filepath.Join(root, "a.txt"), "x")

	result, err := Search(root, "", "")
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if len(result.Entries) != 0 {
		t.Errorf("expected no results for an empty query, got %+v", result.Entries)
	}
}

func TestSearch_ScopedToSubdirectory(t *testing.T) {
	root := setupRoot(t)
	writeFile(t, filepath.Join(root, "report.txt"), "x")
	writeFile(t, filepath.Join(root, "sub", "report.txt"), "x")

	result, err := Search(root, "sub", "report")
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if len(result.Entries) != 1 || result.Entries[0].Path != "sub/report.txt" {
		t.Errorf("expected search to be scoped to sub/, got %+v", result.Entries)
	}
}
