package docker

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"strings"
)

// demuxStream 把 Docker Engine API 沒開 TTY 時的多工串流格式解開成純文字。
//
// 格式(見 https://docs.docker.com/engine/api/v1.43/#tag/Container/operation/ContainerAttach
// 的說明):每個 frame 開頭 8 個 byte —— 第 1 個 byte 是串流種類
// (0=stdin、1=stdout、2=stderr,這裡不特別區分,全部併成同一份文字)、
// 接著 3 個保留 byte、再來 4 個 byte 的 big-endian 長度,後面接對應長度的
// 實際內容。GoNAS 自己建立的容器跟 exec 都刻意不開 TTY(CreateContainerRequest
// 沒有 Tty 欄位、execCreateRequest 固定 Tty:false),所以回應一律是這個格式;
// 如果日後 GoNAS 也支援讓使用者開 TTY 執行,這裡就不能直接套用,需要另外
// 判斷回應是不是純文字直接透傳。
func demuxStream(r io.Reader) (string, error) {
	var out strings.Builder
	reader := bufio.NewReader(r)
	header := make([]byte, 8)
	for {
		if _, err := io.ReadFull(reader, header); err != nil {
			if err == io.EOF {
				break
			}
			return out.String(), fmt.Errorf("reading multiplexed stream header: %w", err)
		}
		size := binary.BigEndian.Uint32(header[4:8])
		if size == 0 {
			continue
		}
		frame := make([]byte, size)
		if _, err := io.ReadFull(reader, frame); err != nil {
			return out.String(), fmt.Errorf("reading multiplexed stream frame: %w", err)
		}
		out.Write(frame)
	}
	return out.String(), nil
}
