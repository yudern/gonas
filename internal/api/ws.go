package api

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
)

// 這是一個「剛好夠用」的 WebSocket(RFC 6455)伺服器端實作,只為了互動式容器
// 終端機這一個用途。GoNAS 刻意維持零第三方依賴(見各套件說明),而 Go 標準
// 函式庫沒有內建 WebSocket 伺服器,所以這裡手寫握手 + 收發 frame 的最小子集:
// 文字/二進位/close/ping/pong,不處理分片續傳(continuation)以外的進階特性
// ——終端機的輸入輸出都是小塊、不分片,夠用。
//
// 安全性:這條連線由 requireAdmin 中介層保護(瀏覽器會隨 WebSocket 握手帶上
// 同源 cookie),所以到這裡的都是已登入的管理員。另外只接受同源的握手(檢查
// Host 與 Origin),擋掉跨站 WebSocket 劫持。

const wsMagicGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// wsConn 是一條已完成握手的 WebSocket 連線。
type wsConn struct {
	conn net.Conn
	rw   *bufio.ReadWriter
}

// wsOpcode 是我們會處理到的 frame 類型。
const (
	wsOpContinuation = 0x0
	wsOpText         = 0x1
	wsOpBinary       = 0x2
	wsOpClose        = 0x8
	wsOpPing         = 0x9
	wsOpPong         = 0xA
)

// wsAcceptKey 依 RFC 6455 算出握手回應的 Sec-WebSocket-Accept 值。
func wsAcceptKey(clientKey string) string {
	h := sha1.New()
	h.Write([]byte(clientKey + wsMagicGUID))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// isWebSocketUpgrade 回報這個請求是不是 WebSocket 升級握手。
func isWebSocketUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket") &&
		strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade") &&
		r.Header.Get("Sec-WebSocket-Key") != ""
}

// upgradeWebSocket 完成 WebSocket 握手,hijack 掉底層 TCP 連線並回傳 wsConn。
// 失敗時已經盡量回了適當的 HTTP 錯誤,呼叫端只要 return 即可。
func upgradeWebSocket(w http.ResponseWriter, r *http.Request) (*wsConn, error) {
	if !isWebSocketUpgrade(r) {
		http.Error(w, "expected a websocket upgrade", http.StatusBadRequest)
		return nil, fmt.Errorf("not a websocket upgrade")
	}
	// 同源檢查:擋跨站 WebSocket 劫持(CSWSH)。Origin 存在時其 host 必須等於
	// 請求的 Host;沒有 Origin(非瀏覽器用戶端)就不擋。
	if origin := r.Header.Get("Origin"); origin != "" {
		if oh := originHost(origin); oh != "" && !strings.EqualFold(oh, r.Host) {
			http.Error(w, "cross-origin websocket rejected", http.StatusForbidden)
			return nil, fmt.Errorf("cross-origin websocket from %q", origin)
		}
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return nil, fmt.Errorf("responsewriter is not a hijacker")
	}
	conn, rw, err := hj.Hijack()
	if err != nil {
		return nil, fmt.Errorf("hijacking connection: %w", err)
	}
	accept := wsAcceptKey(r.Header.Get("Sec-WebSocket-Key"))
	resp := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + accept + "\r\n\r\n"
	if _, err := rw.WriteString(resp); err != nil {
		conn.Close()
		return nil, fmt.Errorf("writing handshake: %w", err)
	}
	if err := rw.Flush(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("flushing handshake: %w", err)
	}
	return &wsConn{conn: conn, rw: rw}, nil
}

func originHost(origin string) string {
	// origin 形如 "http://host:port";取 "//" 後面的 host[:port]。
	if i := strings.Index(origin, "//"); i >= 0 {
		return origin[i+2:]
	}
	return ""
}

// ReadMessage 讀一個完整的資料訊息(合併同一訊息的分片),回傳 opcode 與 payload。
// 控制 frame(ping/close)會就地處理:ping 自動回 pong、close 回 io.EOF。
func (c *wsConn) ReadMessage() (opcode byte, payload []byte, err error) {
	var msgOpcode byte
	var buf []byte
	for {
		fin, op, data, rerr := c.readFrame()
		if rerr != nil {
			return 0, nil, rerr
		}
		switch op {
		case wsOpClose:
			return wsOpClose, nil, io.EOF
		case wsOpPing:
			_ = c.writeFrame(wsOpPong, data)
			continue
		case wsOpPong:
			continue
		case wsOpText, wsOpBinary:
			msgOpcode = op
			buf = append(buf, data...)
		case wsOpContinuation:
			buf = append(buf, data...)
		}
		if fin {
			return msgOpcode, buf, nil
		}
	}
}

// readFrame 讀一個 frame。來自用戶端的 frame 一定有遮罩(RFC 要求),解遮罩後回傳。
func (c *wsConn) readFrame() (fin bool, opcode byte, payload []byte, err error) {
	var hdr [2]byte
	if _, err = io.ReadFull(c.rw, hdr[:]); err != nil {
		return false, 0, nil, err
	}
	fin = hdr[0]&0x80 != 0
	opcode = hdr[0] & 0x0f
	masked := hdr[1]&0x80 != 0
	length := int(hdr[1] & 0x7f)

	switch length {
	case 126:
		var ext [2]byte
		if _, err = io.ReadFull(c.rw, ext[:]); err != nil {
			return false, 0, nil, err
		}
		length = int(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err = io.ReadFull(c.rw, ext[:]); err != nil {
			return false, 0, nil, err
		}
		length = int(binary.BigEndian.Uint64(ext[:]))
	}
	// 上限保護:單一 frame 不該超過合理大小(終端機輸入都很小)。
	if length < 0 || length > 1<<20 {
		return false, 0, nil, fmt.Errorf("websocket frame too large: %d", length)
	}

	var maskKey [4]byte
	if masked {
		if _, err = io.ReadFull(c.rw, maskKey[:]); err != nil {
			return false, 0, nil, err
		}
	}
	payload = make([]byte, length)
	if _, err = io.ReadFull(c.rw, payload); err != nil {
		return false, 0, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= maskKey[i&3]
		}
	}
	return fin, opcode, payload, nil
}

// writeFrame 寫一個(不分片、不遮罩——伺服器→用戶端不遮罩)frame。
func (c *wsConn) writeFrame(opcode byte, payload []byte) error {
	var hdr []byte
	b0 := byte(0x80 | opcode) // FIN + opcode
	n := len(payload)
	switch {
	case n < 126:
		hdr = []byte{b0, byte(n)}
	case n < 1<<16:
		hdr = []byte{b0, 126, byte(n >> 8), byte(n)}
	default:
		hdr = make([]byte, 10)
		hdr[0] = b0
		hdr[1] = 127
		binary.BigEndian.PutUint64(hdr[2:], uint64(n))
	}
	if _, err := c.rw.Write(hdr); err != nil {
		return err
	}
	if _, err := c.rw.Write(payload); err != nil {
		return err
	}
	return c.rw.Flush()
}

// WriteBinary 送一個二進位訊息(容器輸出)。
func (c *wsConn) WriteBinary(p []byte) error { return c.writeFrame(wsOpBinary, p) }

// WriteClose 送一個 close frame 並關連線。
func (c *wsConn) WriteClose() {
	_ = c.writeFrame(wsOpClose, nil)
	_ = c.conn.Close()
}

// Close 直接關底層連線。
func (c *wsConn) Close() error { return c.conn.Close() }
