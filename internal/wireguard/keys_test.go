package wireguard

import (
	"encoding/base64"
	"testing"
)

func TestGenerateKeyPair_ProducesValidBase64_32ByteKeys(t *testing.T) {
	priv, pub, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair returned error: %v", err)
	}

	privBytes, err := base64.StdEncoding.DecodeString(priv)
	if err != nil {
		t.Fatalf("private key is not valid base64: %v", err)
	}
	if len(privBytes) != 32 {
		t.Errorf("private key length = %d bytes, want 32", len(privBytes))
	}

	pubBytes, err := base64.StdEncoding.DecodeString(pub)
	if err != nil {
		t.Fatalf("public key is not valid base64: %v", err)
	}
	if len(pubBytes) != 32 {
		t.Errorf("public key length = %d bytes, want 32", len(pubBytes))
	}
}

func TestGenerateKeyPair_EachCallProducesDifferentKeys(t *testing.T) {
	priv1, pub1, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair returned error: %v", err)
	}
	priv2, pub2, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair returned error: %v", err)
	}
	if priv1 == priv2 {
		t.Error("expected two calls to GenerateKeyPair to produce different private keys")
	}
	if pub1 == pub2 {
		t.Error("expected two calls to GenerateKeyPair to produce different public keys")
	}
}

// TestPublicKeyFromPrivate_MatchesGeneratedPair 是這個檔案裡最重要的一支
// 測試:它確保 PublicKeyFromPrivate 算出來的公鑰,跟 GenerateKeyPair
// 在同一次呼叫裡回傳、由 crypto/ecdh 內部直接算出的公鑰完全一致。
// 這證明「從私鑰重新推導公鑰」這條路徑用的是同一個 X25519 純量乘法,
// 而不是不小心用了另一種不相容的曲線運算,兩者只是「看起來都是 32
// bytes」但實際上對不起來。
func TestPublicKeyFromPrivate_MatchesGeneratedPair(t *testing.T) {
	priv, wantPub, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair returned error: %v", err)
	}

	gotPub, err := PublicKeyFromPrivate(priv)
	if err != nil {
		t.Fatalf("PublicKeyFromPrivate returned error: %v", err)
	}
	if gotPub != wantPub {
		t.Errorf("PublicKeyFromPrivate(priv) = %q, want %q (should match the public key GenerateKeyPair returned)", gotPub, wantPub)
	}
}

func TestPublicKeyFromPrivate_InvalidBase64(t *testing.T) {
	_, err := PublicKeyFromPrivate("not valid base64!!!")
	if err == nil {
		t.Fatal("expected an error for invalid base64 input, got nil")
	}
}

func TestPublicKeyFromPrivate_WrongLengthKey(t *testing.T) {
	// 16 bytes 而不是 X25519 要求的 32 bytes。
	short := base64.StdEncoding.EncodeToString(make([]byte, 16))
	_, err := PublicKeyFromPrivate(short)
	if err == nil {
		t.Fatal("expected an error for a private key of the wrong length, got nil")
	}
}
