package main

import (
	"strings"
	"testing"
)

func TestSignVerifyRoundtrip(t *testing.T) {
	priv, pubHex, err := generateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	manifest := []byte(`{"version":"v0.2.0","assets":{"linux-amd64":{"url":"https://x/gonasd","sha256":"ab"}}}`)

	sig := signManifest(priv, manifest)
	if err := verifyManifest(pubHex, manifest, sig); err != nil {
		t.Fatalf("expected a freshly signed manifest to verify, got %v", err)
	}
}

func TestVerifyFailsOnModifiedManifest(t *testing.T) {
	priv, pubHex, _ := generateKeypair()
	manifest := []byte(`{"version":"v0.2.0"}`)
	sig := signManifest(priv, manifest)

	tampered := []byte(`{"version":"v0.2.1"}`) // 改了一個 byte
	if err := verifyManifest(pubHex, tampered, sig); err == nil {
		t.Error("expected verification to FAIL when the manifest was modified after signing")
	}
}

func TestVerifyFailsOnWrongKey(t *testing.T) {
	priv1, _, _ := generateKeypair()
	_, pubHex2, _ := generateKeypair() // 另一把金鑰的公鑰
	manifest := []byte(`{"version":"v0.2.0"}`)
	sig := signManifest(priv1, manifest)

	if err := verifyManifest(pubHex2, manifest, sig); err == nil {
		t.Error("expected verification to FAIL with a public key that doesn't match the signing key")
	}
}

func TestPrivateKeyEncodeDecodeRoundtrip(t *testing.T) {
	priv, pubHex, _ := generateKeypair()
	encoded := encodePrivateKey(priv)
	decoded, err := decodePrivateKey([]byte(encoded))
	if err != nil {
		t.Fatalf("decoding an encoded private key should succeed, got %v", err)
	}
	if publicKeyHexFromPrivate(decoded) != pubHex {
		t.Error("public key derived from the decoded private key should match the original")
	}
}

func TestDecodePrivateKeyRejectsGarbage(t *testing.T) {
	if _, err := decodePrivateKey([]byte("not base64 !!!")); err == nil {
		t.Error("expected an error for non-base64 private key data")
	}
	if _, err := decodePrivateKey([]byte("dG9vLXNob3J0")); err == nil { // base64 of "too-short"
		t.Error("expected an error for a wrong-length private key")
	}
}

func TestPubHexIs64HexChars(t *testing.T) {
	_, pubHex, _ := generateKeypair()
	if len(pubHex) != 64 {
		t.Errorf("ed25519 public key hex should be 64 chars, got %d", len(pubHex))
	}
	if strings.ToLower(pubHex) != pubHex {
		t.Error("expected lowercase hex")
	}
}

func TestSha256Hex(t *testing.T) {
	// 已知向量:空字串的 SHA256。
	got := sha256Hex(nil)
	want := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if got != want {
		t.Errorf("sha256 of empty = %s, want %s", got, want)
	}
}
