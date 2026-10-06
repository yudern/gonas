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
	if strings.ContainsAny(shell, " \t\n") {
		shell = ""
	}

	ws, err := upgradeWebSocket(w, r)
	if err != nil {
		s.logger.Warn("terminal websocket upgrade failed", "err", err, "containerId", id)
		return
	}
	defer ws.Close()

	// 第六十六輪(使用者實機:Portainer 容器開終端報「exec: "/bin/sh": no such
	// file or directory」):精簡映像不一定有 /bin/sh。沒指定 cmd 時依序找
	// bash → sh → ash → busybox sh,一個都沒有就直接說明「這個映像沒有 shell」,
	// 而不是丟一行 OCI runtime 錯誤。
	if shell == "" {
		found, noShell := s.pickContainerShell(r.Context(), id)
		if noShell {
			_ = ws.WriteBinary([]byte("\r\n[GoNAS] 这个容器的镜像是精简镜像,里面没有任何 shell(/bin/bash、/bin/sh 都不存在),所以无法打开终端。\r\n" +
				"[GoNAS] This container's image has no shell (no /bin/bash or /bin/sh), so a terminal can't be opened.\r\n" +
				"\r\n可以改用「查看日志」「详情」排查;Portainer 这类应用请直接打开它的网页界面管理。\r\n"))
			ws.WriteClose()
			return
		}
		shell = found
	}

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

// shellCandidates 是互動式終端機依序嘗試的 shell。
var shellCandidates = []string{"/bin/bash", "/bin/sh", "/bin/ash", "/busybox/sh"}

// pickContainerShell 找出容器裡第一個存在的 shell。noShell=true 代表確定
// 一個都沒有;查詢本身失敗(舊版 Docker 不支援等)時退回 /bin/sh 照舊嘗試。
func (s *Server) pickContainerShell(ctx context.Context, id string) (shell string, noShell bool) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for _, c := range shellCandidates {
		ok, err := s.docker.PathExists(ctx, id, c)
		if err != nil {
			return "/bin/sh", false
		}
		if ok {
			return c, false
		}
	}
	// 全部 404:也可能是容器本身不存在——那就交給後面的 exec 回報真正的錯。
	if _, err := s.docker.InspectContainer(ctx, id); err != nil {
		return "/bin/sh", false
	}
	return "", true
}
