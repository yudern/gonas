package api

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// 第六十五輪:系統診斷安裝的即時進度。apt 透過 cmdrunner.StreamRunner 一行
// 一行把輸出送進來,這裡保留最後 doctorLogMax 行,並從 apt 的典型輸出推估
// 階段與百分比:
//
//	「X upgraded, Y newly installed …」→ 這次要處理的套件數 N
//	「Get:」→ 下載中;「Unpacking …」→ 解包;「Setting up …」→ 設定
//	百分比 = 10 + 85 × (解包數 + 設定數) / (2N),前 10% 給索引/計算相依。
type doctorProgress struct {
	mu       sync.Mutex
	Apt      string   `json:"apt"`
	Running  bool     `json:"running"`
	Stage    string   `json:"stage"` // prepare / download / unpack / configure / done / failed
	Percent  int      `json:"percent"`
	Lines    []string `json:"lines"`
	Error    string   `json:"error,omitempty"`
	Detail   string   `json:"detail,omitempty"`
	pkgTotal int
	unpacked int
	setup    int
}

const doctorLogMax = 400

var aptSummaryRe = regexp.MustCompile(`(\d+) upgraded, (\d+) newly installed`)

func (p *doctorProgress) begin(apt string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.Apt, p.Running, p.Stage, p.Percent = apt, true, "prepare", 0
	p.Lines, p.Error, p.Detail = []string{}, "", ""
	p.pkgTotal, p.unpacked, p.setup = 0, 0, 0
}

func (p *doctorProgress) line(l string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.Running {
		return
	}
	p.Lines = append(p.Lines, l)
	if len(p.Lines) > doctorLogMax {
		p.Lines = p.Lines[len(p.Lines)-doctorLogMax:]
	}
	if m := aptSummaryRe.FindStringSubmatch(l); m != nil {
		up, _ := strconv.Atoi(m[1])
		nw, _ := strconv.Atoi(m[2])
		p.pkgTotal = up + nw
	}
	switch {
	case strings.HasPrefix(l, "Get:"):
		p.Stage = "download"
	case strings.HasPrefix(l, "Unpacking "):
		p.Stage = "unpack"
		p.unpacked++
	case strings.HasPrefix(l, "Setting up "):
		p.Stage = "configure"
		p.setup++
	}
	pct := 5
	if p.pkgTotal > 0 {
		pct = 10 + 85*(p.unpacked+p.setup)/(2*p.pkgTotal)
	}
	if pct > 99 {
		pct = 99
	}
	if pct > p.Percent {
		p.Percent = pct
	}
}

func (p *doctorProgress) finish(err error, detail string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.Running = false
	if err != nil {
		p.Stage = "failed"
		p.Error = err.Error()
		p.Detail = detail
		return
	}
	p.Stage = "done"
	p.Percent = 100
}

type doctorProgressView struct {
	Apt     string   `json:"apt"`
	Running bool     `json:"running"`
	Stage   string   `json:"stage"`
	Percent int      `json:"percent"`
	Lines   []string `json:"lines"`
	Error   string   `json:"error,omitempty"`
	Detail  string   `json:"detail,omitempty"`
}

func (p *doctorProgress) snapshot() doctorProgressView {
	p.mu.Lock()
	defer p.mu.Unlock()
	return doctorProgressView{
		Apt: p.Apt, Running: p.Running, Stage: p.Stage, Percent: p.Percent,
		Lines: append([]string{}, p.Lines...), Error: p.Error, Detail: p.Detail,
	}
}

// handleDoctorInstallProgress:GET /api/v1/system/doctor/install/progress。
// 前端在等 POST /doctor/install 回來的同時每秒輪詢這支,即時顯示 apt 輸出。
func (s *Server) handleDoctorInstallProgress(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.doctorProg.snapshot())
}
