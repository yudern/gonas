package monitor

import "sync"

// History 是一個固定容量的取樣紀錄，保存最近 N 次 Snapshot 給 Web UI 畫
// 趨勢圖用。刻意不落地到磁碟：這是「最近一小段時間的走勢」，daemon 重啟
// 後從頭開始累積是完全可以接受的行為 —— 跟 state.json 那種「使用者設定,
// 遺失了會讓人困擾」的資料是兩回事,不需要 internal/state 那套原子寫入。
type History struct {
	mu       sync.RWMutex
	samples  []Snapshot
	capacity int
}

// NewHistory 建立一個最多保存 capacity 筆取樣的 History。capacity <= 0
// 會被視為設定錯誤，強制修正成 1，避免呼叫端不小心傳 0 導致 History
// 變成「永遠不記得任何東西」的陷阱。
func NewHistory(capacity int) *History {
	if capacity <= 0 {
		capacity = 1
	}
	return &History{capacity: capacity}
}

// Add 附加一筆新取樣，超過容量時捨棄最舊的。這裡的容量通常是幾十到
// 一兩百筆(例如每 10 秒一筆、保留 20 分鐘就是 120 筆),量很小，直接用
// slice 搬移即可，不需要真的實作一個索引式的 ring buffer。
func (h *History) Add(s Snapshot) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.samples = append(h.samples, s)
	if len(h.samples) > h.capacity {
		h.samples = h.samples[len(h.samples)-h.capacity:]
	}
}

// Snapshot 回傳目前所有取樣的副本，由舊到新排序。回傳副本而不是內部
// slice 本身，避免呼叫端拿到的資料被之後的 Add 悄悄改到。
func (h *History) Snapshot() []Snapshot {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]Snapshot, len(h.samples))
	copy(out, h.samples)
	return out
}

// Latest 回傳最新一筆取樣；還沒有任何取樣時 ok 是 false。
func (h *History) Latest() (Snapshot, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if len(h.samples) == 0 {
		return Snapshot{}, false
	}
	return h.samples[len(h.samples)-1], true
}
