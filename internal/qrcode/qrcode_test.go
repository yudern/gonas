package qrcode

import (
	"strings"
	"testing"
)

// finder 圖樣的左上角 7x7 應該長成標準的「回」字。用來確認矩陣結構正確
// (功能圖樣有擺對位置)——完整的「掃得出來」驗證是用真的 QR 解碼器
// (zbar)對每個版本回頭掃過(見交付說明),這裡固化結構性的不變量。
func hasFinder(m *Matrix, r0, c0 int) bool {
	for dr := 0; dr < 7; dr++ {
		for dc := 0; dc < 7; dc++ {
			want := dr == 0 || dr == 6 || dc == 0 || dc == 6 || (dr >= 2 && dr <= 4 && dc >= 2 && dc <= 4)
			if m.At(r0+dr, c0+dc) != want {
				return false
			}
		}
	}
	return true
}

func TestEncode_Version1Size(t *testing.T) {
	m, err := Encode("HELLO")
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if m.Size != 21 {
		t.Fatalf("expected a version-1 (21x21) matrix for short input, got %d", m.Size)
	}
}

func TestEncode_FinderPatterns(t *testing.T) {
	m, err := Encode("otpauth://totp/GoNAS:alice?secret=ABCDEFGHIJKLMNOP&issuer=GoNAS")
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !hasFinder(m, 0, 0) {
		t.Error("missing top-left finder pattern")
	}
	if !hasFinder(m, 0, m.Size-7) {
		t.Error("missing top-right finder pattern")
	}
	if !hasFinder(m, m.Size-7, 0) {
		t.Error("missing bottom-left finder pattern")
	}
	// 固定深色模組。
	if !m.At(m.Size-8, 8) {
		t.Error("missing the fixed dark module")
	}
}

func TestEncode_PicksLargerVersionsAsNeeded(t *testing.T) {
	// 版本會隨資料長度變大;矩陣邊長是 17+4*version,所以一定是 4 的倍數 +17。
	prev := 0
	for _, n := range []int{5, 60, 120, 180, 213} {
		m, err := Encode(strings.Repeat("A", n))
		if err != nil {
			t.Fatalf("encode len=%d: %v", n, err)
		}
		if (m.Size-17)%4 != 0 {
			t.Fatalf("len=%d: unexpected matrix size %d", n, m.Size)
		}
		if m.Size < prev {
			t.Fatalf("len=%d: version shrank unexpectedly (%d < %d)", n, m.Size, prev)
		}
		prev = m.Size
	}
}

func TestEncode_TooLongErrors(t *testing.T) {
	if _, err := Encode(strings.Repeat("A", 400)); err == nil {
		t.Fatal("expected an error for data that exceeds version 10 capacity")
	}
}

func TestEncode_Deterministic(t *testing.T) {
	const s = "otpauth://totp/GoNAS:bob?secret=ABCDEFGHIJKLMNOP&issuer=GoNAS&period=30"
	a, err := Encode(s)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Encode(s)
	if err != nil {
		t.Fatal(err)
	}
	if a.Size != b.Size {
		t.Fatal("size differs between runs")
	}
	for r := 0; r < a.Size; r++ {
		for c := 0; c < a.Size; c++ {
			if a.At(r, c) != b.At(r, c) {
				t.Fatalf("module (%d,%d) differs between runs — encoding is not deterministic", r, c)
			}
		}
	}
}

func TestEncodeSVG(t *testing.T) {
	svg, err := EncodeSVG("otpauth://totp/GoNAS:alice?secret=ABCDEFGHIJKLMNOP", 4, 4)
	if err != nil {
		t.Fatalf("encode svg: %v", err)
	}
	if !strings.HasPrefix(svg, "<svg") || !strings.HasSuffix(svg, "</svg>") {
		t.Fatalf("output is not a self-contained <svg> document: %.40s...", svg)
	}
	if !strings.Contains(svg, "<path") {
		t.Error("expected dark modules rendered as a <path>")
	}
	if _, err := EncodeSVG("", 4, 4); err == nil {
		t.Error("expected an error for empty input")
	}
}
