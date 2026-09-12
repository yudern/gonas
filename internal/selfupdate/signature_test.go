package selfupdate

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// signedManifestServer 起一個同時提供 manifest 與其 detached .sig 的伺服器。
// manifestBody 是實際回給 /manifest 的位元組;sig 是回給 /manifest.sig 的
// base64 簽章字串(呼叫端自己決定要不要故意簽錯的內容)。
func signedManifestServer(t *testing.T, manifestBody []byte, sigB64 string, serveSig bool) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/manifest", func(w http.ResponseWriter, r *http.Request) {
		w.Write(manifestBody)
	})
	mux.HandleFunc("/manifest.sig", func(w http.ResponseWriter, r *http.Request) {
		if !serveSig {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(sigB64))
	})
	return httptest.NewServer(mux)
}

func mustManifestBody(t *testing.T) []byte {
	t.Helper()
	b, err := json.Marshal(Manifest{Version: "v2.0.0", Assets: map[string]Asset{}})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func withPublicKey(t *testing.T, hexKey string) {
	t.Helper()
	prev := ManifestPublicKeyHex
	ManifestPublicKeyHex = hexKey
	t.Cleanup(func() { ManifestPublicKeyHex = prev })
}

// TestFetchManifest_ValidSignatureAccepted:設定公鑰 + 正確簽章 → 通過。
func TestFetchManifest_ValidSignatureAccepted(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	body := mustManifestBody(t)
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, body))
	withPublicKey(t, hex.EncodeToString(pub))

	srv := signedManifestServer(t, body, sig, true)
	defer srv.Close()

	m, err := FetchManifest(context.Background(), srv.Client(), srv.URL+"/manifest")
	if err != nil {
		t.Fatalf("expected valid signature to be accepted, got error: %v", err)
	}
	if m.Version != "v2.0.0" {
		t.Errorf("unexpected manifest version %q", m.Version)
	}
}

// TestFetchManifest_TamperedManifestRejected:簽章是蓋在原始 body 上,但
// 伺服器回一個「被竄改過」的 manifest → 驗證必須失敗、拒絕更新。
func TestFetchManifest_TamperedManifestRejected(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	original := mustManifestBody(t)
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, original))
	withPublicKey(t, hex.EncodeToString(pub))

	// 攻擊者把 manifest 換成指向惡意 URL 的版本(這裡簡化成改版本號即可
	// 讓 body 位元組不同),但沒有對應私鑰、簽章還是舊的。
	tampered, _ := json.Marshal(Manifest{Version: "v9.9.9-evil", Assets: map[string]Asset{}})
	srv := signedManifestServer(t, tampered, sig, true)
	defer srv.Close()

	_, err = FetchManifest(context.Background(), srv.Client(), srv.URL+"/manifest")
	if err == nil {
		t.Fatal("expected tampered manifest to be REJECTED, but it was accepted")
	}
	if !strings.Contains(err.Error(), "signature verification FAILED") {
		t.Errorf("expected a signature-verification-failed error, got: %v", err)
	}
}

// TestFetchManifest_MissingSignatureRejectedWhenKeySet:設了公鑰但抓不到
// .sig(404)→ fail-closed。
func TestFetchManifest_MissingSignatureRejectedWhenKeySet(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	withPublicKey(t, hex.EncodeToString(pub))

	body := mustManifestBody(t)
	srv := signedManifestServer(t, body, "", false) // 不提供 .sig
	defer srv.Close()

	_, err = FetchManifest(context.Background(), srv.Client(), srv.URL+"/manifest")
	if err == nil {
		t.Fatal("expected missing signature to be rejected when a public key is configured")
	}
}

// TestFetchManifest_NoKeyBackwardCompatible:沒設公鑰 → 完全不碰 .sig,
// 維持原本只有 SHA256 的行為(向後相容)。
func TestFetchManifest_NoKeyBackwardCompatible(t *testing.T) {
	withPublicKey(t, "") // 明確清空

	body := mustManifestBody(t)
	// serveSig=false:如果程式碼在沒公鑰時還去抓 .sig,這裡會 404 導致失敗,
	// 正好驗證「沒公鑰就完全不該碰 .sig」。
	srv := signedManifestServer(t, body, "", false)
	defer srv.Close()

	m, err := FetchManifest(context.Background(), srv.Client(), srv.URL+"/manifest")
	if err != nil {
		t.Fatalf("expected no-key path to succeed without touching .sig, got: %v", err)
	}
	if m.Version != "v2.0.0" {
		t.Errorf("unexpected version %q", m.Version)
	}
}

// TestFetchManifest_InvalidPublicKeyFailsClosed:公鑰設錯(不是合法的
// 32-byte hex)→ 硬失敗,不能悄悄退回不驗章。
func TestFetchManifest_InvalidPublicKeyFailsClosed(t *testing.T) {
	withPublicKey(t, "not-a-valid-hex-key")

	body := mustManifestBody(t)
	srv := signedManifestServer(t, body, "whatever", true)
	defer srv.Close()

	_, err := FetchManifest(context.Background(), srv.Client(), srv.URL+"/manifest")
	if err == nil {
		t.Fatal("expected an invalid embedded public key to fail closed")
	}
}
