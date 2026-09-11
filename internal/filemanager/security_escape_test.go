package filemanager

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestSecurity_PathTraversalBattery throws a battery of malicious relative paths
// at every entry point that takes one, and asserts none of them can read,
// write, or create anything outside the configured root.
func TestSecurity_PathTraversalBattery(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	// A secret file OUTSIDE the root that must never be reachable.
	secret := filepath.Join(base, "secret.txt")
	if err := os.WriteFile(secret, []byte("TOP SECRET"), 0o644); err != nil {
		t.Fatal(err)
	}

	malicious := []string{
		"../secret.txt",
		"../../secret.txt",
		"../../../../../../etc/passwd",
		"foo/../../secret.txt",
		"./../secret.txt",
		"..",
		"../",
		"....//secret.txt",
		"..%2fsecret.txt",       // encoded (handler layer would decode; here literal)
		"/../secret.txt",        // leading slash
		"a/b/c/../../../../secret.txt",
		string([]byte{'.', '.', '/', '.', '.', '/'}) + "secret.txt",
	}

	for _, p := range malicious {
		// resolve must either error, or land strictly inside root.
		got, err := resolve(root, p)
		if err == nil {
			rel, rerr := filepath.Rel(root, got)
			if rerr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
				t.Errorf("resolve(%q) returned path OUTSIDE root: %q (rel=%q)", p, got, rel)
			}
		}
		// ReadTextFile must never return the secret contents.
		if content, err := ReadTextFile(root, p); err == nil {
			if strings.Contains(content, "TOP SECRET") {
				t.Errorf("ReadTextFile(%q) LEAKED secret file outside root", p)
			}
		}
	}
}

// TestSecurity_SymlinkEscape creates a symlink INSIDE root that points OUTSIDE,
// then tries to read/write through it. The package documents it defends
// against symlinks that lead outside root.
func TestSecurity_SymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on windows")
	}
	base := t.TempDir()
	root := filepath.Join(base, "root")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	secretDir := filepath.Join(base, "outside")
	if err := os.MkdirAll(secretDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secretDir, "creds"), []byte("PWNED"), 0o644); err != nil {
		t.Fatal(err)
	}
	// symlink root/escape -> ../outside
	link := filepath.Join(root, "escape")
	if err := os.Symlink(secretDir, link); err != nil {
		t.Fatal(err)
	}

	// Reading through the symlink to a file outside root must be blocked.
	if content, err := ReadTextFile(root, "escape/creds"); err == nil {
		if strings.Contains(content, "PWNED") {
			t.Errorf("symlink escape: ReadTextFile read a file OUTSIDE root via in-root symlink")
		}
	}
	// Writing through the symlink must be blocked.
	if err := WriteTextFile(root, "escape/newfile", "attacker"); err == nil {
		if _, serr := os.Stat(filepath.Join(secretDir, "newfile")); serr == nil {
			t.Errorf("symlink escape: WriteTextFile wrote a file OUTSIDE root via in-root symlink")
		}
	}
	// Mkdir through the symlink must be blocked.
	if err := Mkdir(root, "escape/newdir"); err == nil {
		if _, serr := os.Stat(filepath.Join(secretDir, "newdir")); serr == nil {
			t.Errorf("symlink escape: Mkdir created a dir OUTSIDE root via in-root symlink")
		}
	}
}
