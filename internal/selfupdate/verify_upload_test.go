package selfupdate

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// minimalELF 造出一個「檔頭正確」的最小 ELF 位元組:magic + 小端序標記 +
// e_machine 設成 want。VerifyExecutableForHost 只讀前 20 個 byte,所以這足夠
// 讓它判斷「是不是本機平台的 ELF」,不需要一個真的能跑的執行檔。
func minimalELF(machine uint16) []byte {
	b := make([]byte, 64)
	b[0], b[1], b[2], b[3] = 0x7F, 'E', 'L', 'F'
	b[4] = 2 // ELFCLASS64
	b[5] = 1 // ELFDATA2LSB(小端序)
	b[18] = byte(machine & 0xFF)
	b[19] = byte(machine >> 8)
	return b
}

func machineFor(t *testing.T, goarch string) uint16 {
	t.Helper()
	m, ok := elfMachineForGoarch(goarch)
	if !ok {
		t.Skipf("no ELF machine mapping for GOARCH %q", goarch)
	}
	return m
}

func writeTemp(t *testing.T, content []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "upload.bin")
	if err := os.WriteFile(p, content, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestVerifyExecutableForHost_AcceptsMatchingELF(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("ELF header check only runs on linux")
	}
	p := writeTemp(t, minimalELF(machineFor(t, runtime.GOARCH)))
	if err := VerifyExecutableForHost(p); err != nil {
		t.Errorf("expected a matching-arch ELF to be accepted, got %v", err)
	}
}

func TestVerifyExecutableForHost_RejectsEmpty(t *testing.T) {
	p := writeTemp(t, nil)
	if err := VerifyExecutableForHost(p); err == nil {
		t.Error("expected an empty file to be rejected")
	}
}

func TestVerifyExecutableForHost_RejectsNonELF(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("ELF header check only runs on linux")
	}
	// 一份「看起來像 tar.gz / 文字檔」的內容:沒有 ELF magic。
	p := writeTemp(t, []byte("#!/bin/sh\necho not an elf\n"))
	if err := VerifyExecutableForHost(p); err == nil {
		t.Error("expected a non-ELF file to be rejected")
	}
}

func TestVerifyExecutableForHost_RejectsWrongArch(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("ELF header check only runs on linux")
	}
	// 挑一個「跟本機不同」的架構的 e_machine。
	var wrong uint16 = 0x3E // amd64
	if runtime.GOARCH == "amd64" {
		wrong = 0xB7 // 換成 arm64,確保跟本機不同
	}
	p := writeTemp(t, minimalELF(wrong))
	if err := VerifyExecutableForHost(p); err == nil {
		t.Errorf("expected a wrong-architecture ELF to be rejected on %s", runtime.GOARCH)
	}
}
