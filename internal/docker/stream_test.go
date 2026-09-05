package docker

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

// muxFrame 組出一個 Docker Engine API 多工串流格式的 frame，測試用來模擬
// daemon 端實際會送回來的位元組序列，而不是直接塞純文字進去。
func muxFrame(streamType byte, payload string) []byte {
	header := make([]byte, 8)
	header[0] = streamType
	binary.BigEndian.PutUint32(header[4:8], uint32(len(payload)))
	return append(header, []byte(payload)...)
}

func TestDemuxStream_SingleFrame(t *testing.T) {
	data := muxFrame(1, "hello from stdout\n")
	out, err := demuxStream(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("demuxStream returned error: %v", err)
	}
	if out != "hello from stdout\n" {
		t.Errorf("got %q", out)
	}
}

func TestDemuxStream_MultipleFramesInterleavedStdoutStderr(t *testing.T) {
	var buf bytes.Buffer
	buf.Write(muxFrame(1, "line one (stdout)\n"))
	buf.Write(muxFrame(2, "line two (stderr)\n"))
	buf.Write(muxFrame(1, "line three (stdout)\n"))

	out, err := demuxStream(&buf)
	if err != nil {
		t.Fatalf("demuxStream returned error: %v", err)
	}
	want := "line one (stdout)\nline two (stderr)\nline three (stdout)\n"
	if out != want {
		t.Errorf("got %q, want %q", out, want)
	}
}

func TestDemuxStream_EmptyStreamReturnsEmptyString(t *testing.T) {
	out, err := demuxStream(strings.NewReader(""))
	if err != nil {
		t.Fatalf("demuxStream returned error: %v", err)
	}
	if out != "" {
		t.Errorf("expected empty output, got %q", out)
	}
}

func TestDemuxStream_ZeroLengthFrameIsSkipped(t *testing.T) {
	var buf bytes.Buffer
	buf.Write(muxFrame(1, ""))
	buf.Write(muxFrame(1, "real content\n"))

	out, err := demuxStream(&buf)
	if err != nil {
		t.Fatalf("demuxStream returned error: %v", err)
	}
	if out != "real content\n" {
		t.Errorf("got %q", out)
	}
}

func TestDemuxStream_TruncatedFrameReturnsError(t *testing.T) {
	// 宣告了長度但實際內容被截斷,模擬連線中途斷掉的情況。
	header := make([]byte, 8)
	binary.BigEndian.PutUint32(header[4:8], 100)
	data := append(header, []byte("too short")...)

	if _, err := demuxStream(bytes.NewReader(data)); err == nil {
		t.Fatal("expected error for truncated frame, got nil")
	}
}
