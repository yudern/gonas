package monitor

import "fmt"

// Metric 是一個告警規則能拿來比較的指標。前三個是連續數值(百分比),
// 後兩個是布林狀態 —— 之所以放進同一個型別而不是拆成兩種規則,是因為
// 對呼叫端(API、Web UI)來說「選一個指標、決定觸發條件」是同一個心智
// 模型，沒有必要為了內部實作方便而讓使用者面對兩套不同的表單。
type Metric string

const (
	MetricCPUPercent  Metric = "cpuPercent"  // 系統 CPU 使用率(%)
	MetricMemPercent  Metric = "memPercent"  // 記憶體使用率(%)
	MetricDiskPercent Metric = "diskPercent" // 陣列掛載點使用率(%)
	MetricArrayFailed Metric = "arrayFailed" // 陣列狀態是否為 failed(布林,Comparator/Threshold 無意義)
	MetricSmartFailed Metric = "smartFailed" // 是否有任一顆碟 SMART 檢查沒過(布林,Comparator/Threshold 無意義)
)

func (m Metric) valid() bool {
	switch m {
	case MetricCPUPercent, MetricMemPercent, MetricDiskPercent, MetricArrayFailed, MetricSmartFailed:
		return true
	default:
		return false
	}
}

// isBoolean 回傳這個指標是不是「本身就是 true/false」的狀態型指標
// (陣列/SMART 健康),這種指標不需要 Comparator/Threshold。
func (m Metric) isBoolean() bool {
	return m == MetricArrayFailed || m == MetricSmartFailed
}

// Comparator 是數值型指標的比較方式。
type Comparator string

const (
	ComparatorGT Comparator = ">"
	ComparatorGE Comparator = ">="
	ComparatorLT Comparator = "<"
	ComparatorLE Comparator = "<="
)

func (c Comparator) valid() bool {
	switch c {
	case ComparatorGT, ComparatorGE, ComparatorLT, ComparatorLE:
		return true
	default:
		return false
	}
}

// AlertRule 是使用者設定的一條告警規則，例如「CPU 使用率 > 90% 時告警」
// 或「陣列狀態變成 failed 時告警」。
type AlertRule struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Metric     Metric     `json:"metric"`
	Comparator Comparator `json:"comparator,omitempty"` // 布林型指標可留空
	Threshold  float64    `json:"threshold,omitempty"`  // 布林型指標可留空
	Enabled    bool       `json:"enabled"`
}

// Validate 檢查規則本身是不是合法設定，不涉及任何即時資料。
func (r AlertRule) Validate() error {
	if r.Name == "" {
		return fmt.Errorf("alert rule name is required")
	}
	if !r.Metric.valid() {
		return fmt.Errorf("unknown metric %q", r.Metric)
	}
	if r.Metric.isBoolean() {
		return nil // arrayFailed/smartFailed 不需要 Comparator/Threshold
	}
	if !r.Comparator.valid() {
		return fmt.Errorf("metric %q requires a comparator (one of > >= < <=), got %q", r.Metric, r.Comparator)
	}
	if r.Threshold < 0 {
		return fmt.Errorf("threshold must not be negative, got %v", r.Threshold)
	}
	return nil
}

// Facts 是評估一輪告警規則所需要的即時資料快照,把「系統資源」跟
// 「儲存健康狀態」放在同一個結構裡,是因為它們通常來自同一次輪詢
// (見 internal/api 裡把 Poller.onSample 接上告警引擎的地方)。
type Facts struct {
	Snapshot    Snapshot
	ArrayFailed bool
	SmartFailed bool
}

// evaluateRule 判斷在目前的 Facts 底下，這條規則是不是處於「觸發中」的
// 狀態。純函式，不涉及任何防抖/去重邏輯(那是 AlertEngine 的職責),
// 方便單獨測試「這條規則對這組數字該不該觸發」這件事本身對不對。
func evaluateRule(r AlertRule, f Facts) (bool, error) {
	switch r.Metric {
	case MetricCPUPercent:
		return compareValue(f.Snapshot.CPUPercent, r.Comparator, r.Threshold)
	case MetricMemPercent:
		return compareValue(f.Snapshot.MemPercent, r.Comparator, r.Threshold)
	case MetricDiskPercent:
		return compareValue(f.Snapshot.DiskPercent, r.Comparator, r.Threshold)
	case MetricArrayFailed:
		return f.ArrayFailed, nil
	case MetricSmartFailed:
		return f.SmartFailed, nil
	default:
		return false, fmt.Errorf("unknown metric %q", r.Metric)
	}
}

func compareValue(value float64, cmp Comparator, threshold float64) (bool, error) {
	switch cmp {
	case ComparatorGT:
		return value > threshold, nil
	case ComparatorGE:
		return value >= threshold, nil
	case ComparatorLT:
		return value < threshold, nil
	case ComparatorLE:
		return value <= threshold, nil
	default:
		return false, fmt.Errorf("unknown comparator %q", cmp)
	}
}
