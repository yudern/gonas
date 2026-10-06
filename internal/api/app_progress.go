package api

import (
	"sort"
	"strings"
	"sync"

	"github.com/bng147/gonas/internal/docker"
)

// 第六十五輪(使用者:「所有的安裝是不是沒進度條和詳細信息」)。原本安裝/
// 更新只回報 docker 最後一行狀態字串(例如「Downloading」),看不出下載了
// 多少、還要多久、現在在哪一步。這裡把 docker 的結構化拉取事件按「服務/層」
// 彙整成:每層進度、總百分比、已下載/總位元組、目前步驟,以及一份精簡日誌
// (只記「狀態有變」的行,不記每一次位元組更新),快照寫進 appOpStatus 給
// 前端輪詢。

// layerProgress 是一個映像層的進度。
type layerProgress struct {
	Service string `json:"service"`
	ID      string `json:"id"`
	Status  string `json:"status"`
	Percent int    `json:"percent"`
	Current int64  `json:"current,omitempty"`
	Total   int64  `json:"total,omitempty"`

	dlDone   bool
	dlCur    int64
	dlTotal  int64
	exFrac   float64
	complete bool
}

const appLogMax = 200

type appProgress struct {
	mu     sync.Mutex
	base   appOpStatus
	layers map[string]*layerProgress
	order  []string
	log    []string
}

func newAppProgress(appID, action string) *appProgress {
	return &appProgress{
		base:   appOpStatus{AppID: appID, Action: action, Stage: "running"},
		layers: map[string]*layerProgress{},
	}
}

func (p *appProgress) appendLog(line string) {
	p.log = append(p.log, line)
	if len(p.log) > appLogMax {
		p.log = p.log[len(p.log)-appLogMax:]
	}
}

// step 記錄某服務進入哪一步(pull/create/start/check)。
func (p *appProgress) step(svc, step string) appOpStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.base.Service = svc
	p.base.Step = step
	p.appendLog("[" + svc + "] step: " + step)
	return p.snapshotLocked()
}

// event 吃一筆 docker 拉取事件。
func (p *appProgress) event(svc string, ev docker.PullEvent) appOpStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.base.Service = svc
	p.base.Progress = ev.Status
	if ev.ID == "" || !looksLikeLayerEvent(ev.Status) {
		p.appendLog("[" + svc + "] " + strings.TrimSpace(ev.Status+" "+ev.ID))
		return p.snapshotLocked()
	}
	key := svc + "/" + ev.ID
	l, ok := p.layers[key]
	if !ok {
		l = &layerProgress{Service: svc, ID: ev.ID}
		p.layers[key] = l
		p.order = append(p.order, key)
	}
	if l.Status != ev.Status {
		p.appendLog("[" + svc + "] " + ev.ID + ": " + ev.Status)
	}
	l.Status = ev.Status
	st := strings.ToLower(ev.Status)
	switch {
	case strings.HasPrefix(st, "downloading"):
		l.dlCur, l.dlTotal = ev.Current, ev.Total
	case strings.HasPrefix(st, "verifying checksum"), strings.HasPrefix(st, "download complete"):
		l.dlDone = true
		if l.dlTotal > 0 {
			l.dlCur = l.dlTotal
		}
	case strings.HasPrefix(st, "extracting"):
		l.dlDone = true
		if l.dlTotal > 0 {
			l.dlCur = l.dlTotal
		}
		if ev.Total > 0 {
			l.exFrac = float64(ev.Current) / float64(ev.Total)
		}
	case strings.HasPrefix(st, "pull complete"), strings.HasPrefix(st, "already exists"):
		l.complete = true
		l.dlDone = true
		if l.dlTotal > 0 {
			l.dlCur = l.dlTotal
		}
		l.exFrac = 1
	}
	l.Current, l.Total = ev.Current, ev.Total
	return p.snapshotLocked()
}

// looksLikeLayerEvent:帶 ID 但其實是整體訊息的行(例如 "Pulling from
// library/nginx" 的 ID 是 tag)不當成層。
func looksLikeLayerEvent(status string) bool {
	st := strings.ToLower(status)
	return !strings.HasPrefix(st, "pulling from") && !strings.HasPrefix(st, "digest:") && !strings.HasPrefix(st, "status:")
}

func (l *layerProgress) fraction() float64 {
	if l.complete {
		return 1
	}
	dl := 0.0
	switch {
	case l.dlDone:
		dl = 1
	case l.dlTotal > 0:
		dl = float64(l.dlCur) / float64(l.dlTotal)
	}
	return 0.8*dl + 0.2*l.exFrac
}

func (p *appProgress) finish(stage, errMsg string) appOpStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.base.Stage = stage
	p.base.Error = errMsg
	if stage == "done" {
		p.base.Percent = 100
		p.appendLog("done")
	} else if errMsg != "" {
		p.appendLog("error: " + errMsg)
	}
	return p.snapshotLocked()
}

func (p *appProgress) snapshotLocked() appOpStatus {
	st := p.base
	var sum float64
	var dl, total int64
	layers := make([]layerProgress, 0, len(p.order))
	for _, k := range p.order {
		l := p.layers[k]
		f := l.fraction()
		sum += f
		if l.dlTotal > 0 {
			total += l.dlTotal
			dl += l.dlCur
		}
		c := *l
		c.Percent = int(f*100 + 0.5)
		layers = append(layers, c)
	}
	sort.SliceStable(layers, func(i, j int) bool { return layers[i].Service < layers[j].Service })
	st.Layers = layers
	st.Downloaded, st.TotalBytes = dl, total
	if len(layers) > 0 && st.Stage == "running" {
		st.Percent = int(sum/float64(len(layers))*100 + 0.5)
		if st.Percent > 99 {
			st.Percent = 99 // 拉完還有建立/啟動容器,100% 留給真正完成
		}
	}
	st.Log = append([]string(nil), p.log...)
	return st
}
