package ups

import (
	"context"
	"errors"
	"testing"
)

type fakeRunner struct {
	out map[string]string // args-joined -> stdout
	err error
}

func (f *fakeRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	key := name + " " + joinArgs(args)
	return []byte(f.out[key]), nil
}
func (f *fakeRunner) RunWithStdin(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
	return nil, errors.New("unexpected")
}
func joinArgs(a []string) string {
	s := ""
	for i, x := range a {
		if i > 0 {
			s += " "
		}
		s += x
	}
	return s
}

const sampleOnline = `battery.charge: 100
battery.runtime: 3000
device.model: Back-UPS 650
ups.load: 15
ups.status: OL
`

const sampleOnBattery = `battery.charge: 40
battery.runtime: 600
device.model: Back-UPS 650
ups.load: 20
ups.status: OB
`

const sampleLowBattery = `battery.charge: 8
battery.runtime: 90
ups.status: OB LB
`

func TestParseUPSC_Online(t *testing.T) {
	s := parseUPSC("ups", []byte(sampleOnline))
	if !s.Present || s.OnBattery || s.LowBattery {
		t.Fatalf("online: unexpected flags %+v", s)
	}
	if s.Model != "Back-UPS 650" || s.Status != "OL" {
		t.Errorf("model/status wrong: %+v", s)
	}
	if s.BatteryCharge == nil || *s.BatteryCharge != 100 {
		t.Errorf("charge wrong: %+v", s.BatteryCharge)
	}
	if s.RuntimeSeconds == nil || *s.RuntimeSeconds != 3000 {
		t.Errorf("runtime wrong: %+v", s.RuntimeSeconds)
	}
	if s.LoadPercent == nil || *s.LoadPercent != 15 {
		t.Errorf("load wrong: %+v", s.LoadPercent)
	}
}

func TestParseUPSC_Flags(t *testing.T) {
	ob := parseUPSC("ups", []byte(sampleOnBattery))
	if !ob.OnBattery || ob.LowBattery {
		t.Errorf("on-battery flags wrong: %+v", ob)
	}
	lb := parseUPSC("ups", []byte(sampleLowBattery))
	if !lb.OnBattery || !lb.LowBattery {
		t.Errorf("low-battery flags wrong: %+v", lb)
	}
}

func TestShouldShutdown(t *testing.T) {
	online := parseUPSC("ups", []byte(sampleOnline))
	onbat := parseUPSC("ups", []byte(sampleOnBattery))
	low := parseUPSC("ups", []byte(sampleLowBattery))

	// 市電正常:永不關機。
	if ShouldShutdown(online, 0) || ShouldShutdown(online, 3600) {
		t.Error("must never shut down while on line power")
	}
	// 靠電池但電量還可以、沒設門檻:不關。
	if ShouldShutdown(onbat, 0) {
		t.Error("on battery without LB and no threshold should not shut down")
	}
	// 靠電池 + LB:關。
	if !ShouldShutdown(low, 0) {
		t.Error("on battery + low-battery flag should shut down")
	}
	// 靠電池 + 續航(600s)低於門檻(900s):關。
	if !ShouldShutdown(onbat, 900) {
		t.Error("on battery + runtime below threshold should shut down")
	}
	// 靠電池 + 續航(600s)高於門檻(300s):不關。
	if ShouldShutdown(onbat, 300) {
		t.Error("on battery + runtime above threshold should not shut down")
	}
	// 沒查到 UPS:不關。
	if ShouldShutdown(Status{Present: false, OnBattery: true, LowBattery: true}, 0) {
		t.Error("absent UPS must never trigger shutdown")
	}
}

func TestListAndQuery(t *testing.T) {
	r := &fakeRunner{out: map[string]string{
		"upsc -l":    "myups\nother\n",
		"upsc myups": sampleOnBattery,
	}}
	names, err := List(context.Background(), r)
	if err != nil || len(names) != 2 || names[0] != "myups" {
		t.Fatalf("List wrong: %v %v", names, err)
	}
	s, err := Query(context.Background(), r, "myups")
	if err != nil || !s.OnBattery || s.UPSName != "myups" {
		t.Fatalf("Query wrong: %+v %v", s, err)
	}
}

func TestQuery_UpscMissing(t *testing.T) {
	r := &fakeRunner{err: errors.New(`exec: "upsc": not found`)}
	if _, err := Query(context.Background(), r, "ups"); err == nil {
		t.Error("expected error when upsc is missing")
	}
}
