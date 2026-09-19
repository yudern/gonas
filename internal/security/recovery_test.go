package security

import "testing"

func TestGenerateRecoveryCodes(t *testing.T) {
	plain, hashed, err := GenerateRecoveryCodes(0)
	if err != nil {
		t.Fatalf("GenerateRecoveryCodes: %v", err)
	}
	if len(plain) != recoveryCodeCount || len(hashed) != recoveryCodeCount {
		t.Fatalf("expected %d codes, got plain=%d hashed=%d", recoveryCodeCount, len(plain), len(hashed))
	}
	// 每組明文的雜湊要對得上對應的 hashed;且明文之間不重複
	seen := map[string]bool{}
	for i, p := range plain {
		if seen[p] {
			t.Errorf("duplicate plaintext code %q", p)
		}
		seen[p] = true
		if HashRecoveryCode(p) != hashed[i] {
			t.Errorf("hash mismatch for code %d", i)
		}
		// 明文不能等於雜湊(確定有雜湊)
		if p == hashed[i] {
			t.Errorf("plaintext equals hash for code %d", i)
		}
	}
}

func TestMatchRecoveryCode(t *testing.T) {
	plain, hashed, _ := GenerateRecoveryCodes(5)
	// 正規化:大小寫、連字號、空白都不該影響比對
	idx, ok := MatchRecoveryCode(plain[2], hashed)
	if !ok || idx != 2 {
		t.Fatalf("expected match at 2, got idx=%d ok=%v", idx, ok)
	}
	messy := "  " + plain[2] + "  "
	if _, ok := MatchRecoveryCode(messy, hashed); !ok {
		t.Errorf("whitespace-padded code should still match")
	}
	if _, ok := MatchRecoveryCode("not-a-real-code", hashed); ok {
		t.Errorf("bogus code should not match")
	}
	if _, ok := MatchRecoveryCode("", hashed); ok {
		t.Errorf("empty input should not match")
	}
}

func TestHashRecoveryCode_Deterministic(t *testing.T) {
	if HashRecoveryCode("Abc-DE fg") != HashRecoveryCode("abcdefg") {
		t.Errorf("normalization should make these hash equal")
	}
}
