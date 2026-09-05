package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// execCreateRequest 對應 `POST /containers/{id}/exec` 的請求 body,刻意只
// 給 GoNAS 用得到的欄位:固定不接 stdin、不開 TTY(理由跟 CreateContainerRequest
// 一樣，見 stream.go 的說明),只需要 Cmd。
type execCreateRequest struct {
	AttachStdin  bool     `json:"AttachStdin"`
	AttachStdout bool     `json:"AttachStdout"`
	AttachStderr bool     `json:"AttachStderr"`
	Tty          bool     `json:"Tty"`
	Cmd          []string `json:"Cmd"`
}

type execCreateResponse struct {
	ID string `json:"Id"`
}

// execStartRequest 對應 `POST /exec/{id}/start`。Detach:false 讓 daemon
// 把整個執行過程的輸出即時串流回這個 HTTP 回應,而不是背景執行、回應直接
// 結束——這裡要的是「執行一次指令、等它跑完、把輸出整包拿回來」這種
// 一次性診斷用途,不是互動式終端機(互動終端機需要雙向 hijack 連線，
// 複雜度高很多，GoNAS 目前的診斷需求用一次性執行就夠)。
type execStartRequest struct {
	Detach bool `json:"Detach"`
	Tty    bool `json:"Tty"`
}

// ExecInspect 是 `GET /exec/{id}/json` 的精簡結果，執行完之後用來確認退出碼。
type ExecInspect struct {
	Running  bool `json:"Running"`
	ExitCode int  `json:"ExitCode"`
}

// ExecResult 是一次 ExecInContainer 呼叫的完整結果。
type ExecResult struct {
	Output   string // 已解開多工串流格式的 stdout+stderr 合併文字
	ExitCode int
}

// ErrExecEmptyCommand 在呼叫端沒有提供任何指令時回傳，避免打一個注定失敗的
// 空 Cmd 給 Docker daemon。
var ErrExecEmptyCommand = errors.New("exec command must not be empty")

// ExecInContainer 在一個執行中的容器裡跑一次指令並回傳完整輸出跟退出碼,
// 等同 `docker exec <container> <cmd...>`。這是一次性執行(等指令跑完才
// 回傳),不是持續連線的互動式終端機 —— 對「這個容器到底裝了什麼、設定檔
// 長怎樣、程序有沒有在跑」這類一次性診斷已經足夠，且不需要處理終端機
// 跳脫序列/視窗大小這些互動終端機才有的複雜度。
func (c *Client) ExecInContainer(ctx context.Context, containerID string, cmd []string) (ExecResult, error) {
	if len(cmd) == 0 {
		return ExecResult{}, ErrExecEmptyCommand
	}

	var created execCreateResponse
	createReq := execCreateRequest{AttachStdout: true, AttachStderr: true, Cmd: cmd}
	if err := c.doJSON(ctx, "POST", "/containers/"+containerID+"/exec", createReq, &created); err != nil {
		return ExecResult{}, fmt.Errorf("creating exec for container %s: %w", containerID, err)
	}

	startBody, err := json.Marshal(execStartRequest{})
	if err != nil {
		return ExecResult{}, fmt.Errorf("marshaling exec start request: %w", err)
	}
	resp, err := c.do(ctx, "POST", "/exec/"+created.ID+"/start", bytes.NewReader(startBody))
	if err != nil {
		return ExecResult{}, fmt.Errorf("starting exec %s in container %s: %w", created.ID, containerID, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ExecResult{}, decodeAPIError(resp)
	}

	output, err := demuxStream(resp.Body)
	if err != nil {
		return ExecResult{Output: output}, fmt.Errorf("reading exec output for container %s: %w", containerID, err)
	}

	var inspect ExecInspect
	if err := c.doJSON(ctx, "GET", "/exec/"+created.ID+"/json", nil, &inspect); err != nil {
		// 指令本身已經跑完、輸出也拿到了，只是查不到退出碼——回傳已經拿到的
		// 輸出並附上錯誤，讓呼叫端自己決定要不要接受「有輸出但退出碼未知」。
		return ExecResult{Output: output}, fmt.Errorf("inspecting exec %s in container %s: %w", created.ID, containerID, err)
	}

	return ExecResult{Output: output, ExitCode: inspect.ExitCode}, nil
}
