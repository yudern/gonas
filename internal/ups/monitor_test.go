package ups

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
)

type monRunner struct{ out string }

func (m *monRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return []byte(m.out), nil
}
func (m *monRunner) RunWithStdin(ctx context.Context, s []byte, name string, args ...string) ([]byte, error) {
	return nil, errors.New("no")
}

func newMon(out string, cfg MonitorConfig, fired *bool) *Monitor {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewMonitor(logger, &monRunner{out: out}, 0, func() MonitorConfig { return cfg }, func() { *fired = true })
}

func TestMonitor_TriggersOnlyWhenLowAndEnabled(t *testing.T) {
	cfgOn := MonitorConfig{Enabled: true, ShutdownOnLowBattery: true, UPSName: "ups"}

	// 市電正常:不關。
	var fired bool
	if newMon(sampleOnline, cfgOn, &fired).evaluate() || fired {
		t.Error("online power must not trigger shutdown")
	}

	// 靠電池 + 低電量:關,且 onShutdown 被呼叫。
	fired = false
	m := newMon(sampleLowBattery, cfgOn, &fired)
	if !m.evaluate() || !fired {
		t.Error("on battery + low should trigger shutdown")
	}
	// 已觸發後不再重複觸發。
	fired = false
	if m.evaluate() || fired {
		t.Error("must not fire twice")
	}
}

func TestMonitor_RespectsConfigGates(t *testing.T) {
	var fired bool
	// 停用自動關機:即使低電量也不關。
	cfg := MonitorConfig{Enabled: true, ShutdownOnLowBattery: false, UPSName: "ups"}
	if newMon(sampleLowBattery, cfg, &fired).evaluate() || fired {
		t.Error("shutdownOnLowBattery=false must not trigger")
	}
	// 整個停用:不關。
	cfg = MonitorConfig{Enabled: false, ShutdownOnLowBattery: true, UPSName: "ups"}
	if newMon(sampleLowBattery, cfg, &fired).evaluate() || fired {
		t.Error("disabled must not trigger")
	}
	// 沒設定 UPS 名稱:不關。
	cfg = MonitorConfig{Enabled: true, ShutdownOnLowBattery: true, UPSName: ""}
	if newMon(sampleLowBattery, cfg, &fired).evaluate() || fired {
		t.Error("empty UPS name must not trigger")
	}
}
