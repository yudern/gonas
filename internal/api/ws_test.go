package api

import (
	"bufio"
	"bytes"
	"testing"
)

// RFC 6455 的範例:key dGhlIHNhbXBsZSBub25jZQ== 的 accept 應為
// s3pPLMBiTxaQ9kYGzzhZRbK+xOo=。
func TestWSAcceptKey(t *testing.T) {
	got := wsAcceptKey("dGhlIHNhbXBsZSBub25jZQ==")
	if got != "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=" {
		t.Errorf("wsAcceptKey = %q, want s3pPLMBiTxaQ9kYGzzhZRbK+xOo=", got)
	}
}

func TestWSReadMaskedClientFrame(t *testing.T) {
	// 組一個「用戶端 → 伺服器」的遮罩文字 frame,內容 "hi"。
	mask := []byte{0x01, 0x02, 0x03, 0x04}
	payload := []byte("hi")
	masked := make([]byte, len(payload))
	for i := range payload {
		masked[i] = payload[i] ^ mask[i&3]
	}
	frame := []byte{0x81, 0x82} // FIN+text, masked+len2
	frame = append(frame, mask...)
	frame = append(frame, masked...)

	c := &wsConn{rw: bufio.NewReadWriter(bufio.NewReader(bytes.NewReader(frame)), bufio.NewWriter(&bytes.Buffer{}))}
	op, data, err := c.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage error: %v", err)
	}
	if op != wsOpText || string(data) != "hi" {
		t.Errorf("got op=%d data=%q, want text 'hi'", op, data)
	}
}

func TestWSWriteServerFrameUnmasked(t *testing.T) {
	var out bytes.Buffer
	c := &wsConn{rw: bufio.NewReadWriter(bufio.NewReader(bytes.NewReader(nil)), bufio.NewWriter(&out))}
	if err := c.WriteBinary([]byte("ok")); err != nil {
		t.Fatal(err)
	}
	got := out.Bytes()
	// 期望:0x82 (FIN+binary), 0x02 (len=2, 未遮罩), 'o','k'
	want := []byte{0x82, 0x02, 'o', 'k'}
	if !bytes.Equal(got, want) {
		t.Errorf("frame = % x, want % x", got, want)
	}
}

func TestWSPingAutoPong(t *testing.T) {
	// 用戶端送一個遮罩 ping(無 payload),ReadMessage 應自動回 pong 並繼續
	// 等下一個資料 frame。這裡 ping 後面接一個文字 frame "x" 驗證它會跳過 ping。
	var out bytes.Buffer
	mask := []byte{0x09, 0x08, 0x07, 0x06}
	ping := []byte{0x89, 0x80, mask[0], mask[1], mask[2], mask[3]} // FIN+ping, masked len0
	xmasked := []byte{'x' ^ mask[0]}
	text := append([]byte{0x81, 0x81, mask[0], mask[1], mask[2], mask[3]}, xmasked...)
	in := append(append([]byte{}, ping...), text...)

	c := &wsConn{rw: bufio.NewReadWriter(bufio.NewReader(bytes.NewReader(in)), bufio.NewWriter(&out))}
	op, data, err := c.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage error: %v", err)
	}
	if op != wsOpText || string(data) != "x" {
		t.Errorf("got op=%d data=%q, want text 'x'", op, data)
	}
	// out 應該含一個 pong frame(0x8A, 0x00)。
	if !bytes.Contains(out.Bytes(), []byte{0x8A, 0x00}) {
		t.Errorf("expected an auto pong frame in output, got % x", out.Bytes())
	}
}

func TestOriginHost(t *testing.T) {
	if originHost("http://nas.local:8291") != "nas.local:8291" {
		t.Errorf("originHost mismatch")
	}
	if originHost("garbage") != "" {
		t.Errorf("expected empty for malformed origin")
	}
}
