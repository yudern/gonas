package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// terminalControlMsg 是前端透過「文字」frame 傳來的控制訊息(目前只有 resize)。
// 容器的鍵盤輸入則走「二進位」frame,原樣寫進容器 stdin。這樣控制訊息與輸入
// 資料用 frame 類型天然分流,不需要在資料流裡夾帶跳脫序列。
type terminalControlMsg struct {
	Type string `json:"type"`           // "resize"
	Cols int    `json:"cols,omitempty"` // 視窗寬(字元數)
	Rows int    `json:"rows,omitempty"` // 視窗高(列數)
}

// handleContainerTerminal 把一條 WebSocket 連線橋接到容器裡一個帶 TTY 的 exec
// (等同 docker exec -it <container> <shell>),實現網頁互動式終端機。requireAdmin。
//
// 協定:二進位 frame = 鍵盤輸入(寫進容器 stdin);文字 frame = JSON 控制訊息
// (目前只有視窗 resize);容器輸出以二進位 frame 送回前端。
func (s *Server) handleContainerTerminal(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.docker == nil {
		http.Error(w, "docker unavailable", http.StatusServiceUnavailable)
		return
	}

	// 要執行的 shell:預設 /bin/sh(幾乎所有映像都有);允許用 ?cmd= 指定
	// (例如 /bin/bash)。只收單一執行檔路徑,不接受帶參數的任意字串(避免
	// 把使用者輸入直接拼成指令),要跑別的直接在終端機裡輸入即可。
	shell := r.URL.Query().Get("cmd")
	if shell == "" || strings.ContainsAny(shell, " \t\n") {
		shell = "/bin/sh"
	}

	ws, err := upgradeWebSocket(w, r)
	if err != nil {
		s.logger.Warn("terminal websocket upgrade failed", "err", err, "containerId", id)
		return
	}
	defer ws.Close()

	exec, err := s.docker.StartInteractiveExec(r.Context(), id, []string{shell})
	if err != nil {
		s.logger.Warn("starting interactive exec failed", "err", err, "containerId", id)
		_ = ws.WriteBinary([]byte("\r\n[GoNAS] 无法进入容器终端: " + err.Error() + "\r\n"))
		ws.WriteClose()
		return
	}
	defer exec.Close()

	// 容器輸出 → WebSocket。獨立 goroutine,EOF(容器裡的 shell 退出)時關掉
	// WebSocket,讓下面的讀取迴圈也跟著結束。
	outputDone := make(chan struct{})
	go func() {
		defer close(outputDone)
		buf := make([]byte, 4096)
		for {
			n, rerr := exec.Reader.Read(buf)
			if n > 0 {
				if werr := ws.WriteBinary(buf[:n]); werr != nil {
					return
				}
			}
			if rerr != nil {
				return
			}
		}
	}()

	// WebSocket → 容器 stdin / 控制訊息。
	readLoop := func() {
		for {
			op, data, rerr := ws.ReadMessage()
			if rerr != nil {
				return
			}
			switch op {
			case wsOpText:
				var msg terminalControlMsg
				if json.Unmarshal(data, &msg) == nil && msg.Type == "resize" {
					// resize 用脫鉤的 context,不綁請求(請求 context 在連線期間
					// 一直有效,但 resize 應該獨立、快速完成)。
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					_ = s.docker.ResizeExec(ctx, exec.ExecID, msg.Rows, msg.Cols)
					cancel()
				}
			case wsOpBinary:
				if _, werr := exec.Conn.Write(data); werr != nil {
					return
				}
			case wsOpClose:
				return
			}
		}
	}

	// 任一方向結束就收攤:關掉 exec 連線會讓 output goroutine 的 Read 返回,
	// 關掉 ws 會讓 readLoop 返回。
	done := make(chan struct{})
	go func() { readLoop(); close(done) }()

	select {
	case <-outputDone:
	case <-done:
	case <-r.Context().Done():
	}
	_ = exec.Close()
	ws.WriteClose()
	// 確保兩個 goroutine 都收尾(避免洩漏):關連線後它們的 I/O 會很快返回。
	<-outputDone
	// readLoop 可能還卡在 ReadMessage;關 ws.conn 已在 WriteClose 做了,等它返回。
	<-done
}
