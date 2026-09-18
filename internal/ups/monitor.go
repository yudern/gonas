package ups

import (
	"context"
	"log/slog"
	"time"

	"github.com/bng147/gonas/internal/safe"
)

// MonitorConfig 是背景監控每一輪要用的即時設定。刻意跟 state.UPSConfig 分開,
// 讓 internal/ups 不必反過來依賴 internal/state(避免套件循環);呼叫端每輪
// 用 getConfig 回呼把當下的持久化設定轉成這個型別餵進來。
type MonitorConfig struct {
	Enabled                 bool
	ShutdownOnLowBattery    bool
	UPSName                 string
	RuntimeThresholdSeconds int
}

// Monitor 是「市電中斷且電量過低就安全關機」的背景守護。每 interval 讀一次
// 當下設定,只有在啟用且開了自動關機時才去 upsc 查一次狀態;一旦判定該關機
// (見 ShouldShutdown),呼叫 onShutdown 並把自己標記成已觸發,之後不再重複
// 觸發(避免對系統連下好幾次關機指令)。
type Monitor struct {
	logger     *slog.Logger
	runner     Runner
	interval   time.Duration
	getConfig  func() MonitorConfig
	onShutdown func()

	cancel context.CancelFunc
	done   chan struct{}
	fired  bool
}

func NewMonitor(logger *slog.Logger, runner Runner, interval time.Duration, getConfig func() MonitorConfig, onShutdown func()) *Monitor {
	return &Monitor{logger: logger, runner: runner, interval: interval, getConfig: getConfig, onShutdown: onShutdown}
}

func (m *Monitor) Start(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	m.cancel = cancel
	m.done = make(chan struct{})
	go func() {
		defer close(m.done)
		ticker := time.NewTicker(m.interval)
		defer ticker.Stop()
		for {
			// 每一輪包 recover:查 UPS/送關機任何一步意外 panic 都不該
			// 拖垮 daemon,只記一筆 log、下一輪照常。見 internal/safe。
			safe.Run(m.logger, "ups-monitor", m.checkOnce)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

// checkOnce 是可單獨測試的一輪判斷。回傳是否觸發了關機(測試用)。
func (m *Monitor) checkOnce() {
	m.evaluate()
}

func (m *Monitor) evaluate() bool {
	cfg := m.getConfig()
	if !cfg.Enabled || !cfg.ShutdownOnLowBattery || cfg.UPSName == "" {
		return false
	}
	if m.fired {
		return false
	}
	s, err := Query(context.Background(), m.runner, cfg.UPSName)
	if err != nil {
		m.logger.Debug("ups-monitor: query failed, skipping this round", "ups", cfg.UPSName, "err", err)
		return false
	}
	if ShouldShutdown(s, cfg.RuntimeThresholdSeconds) {
		m.fired = true
		m.logger.Warn("ups-monitor: UPS on battery and low — triggering safe shutdown",
			"ups", cfg.UPSName, "status", s.Status, "batteryCharge", derefInt(s.BatteryCharge), "runtimeSeconds", derefInt(s.RuntimeSeconds))
		if m.onShutdown != nil {
			m.onShutdown()
		}
		return true
	}
	return false
}

func (m *Monitor) Stop() {
	if m.cancel == nil {
		return
	}
	m.cancel()
	<-m.done
}

func derefInt(p *int) int {
	if p == nil {
		return -1
	}
	return *p
}
