package api

import (
	"errors"
	"testing"
)

func TestDoctorProgress_ParsesAptStages(t *testing.T) {
	var p doctorProgress
	p.begin("mergerfs")
	p.line("Reading package lists...")
	p.line("0 upgraded, 2 newly installed, 0 to remove and 0 not upgraded.")
	p.line("Get:1 file:/var/lib/gonas-offline-debs ./ fuse3 3.14")
	if s := p.snapshot(); s.Stage != "download" {
		t.Errorf("stage = %q, want download", s.Stage)
	}
	p.line("Unpacking fuse3 (3.14) ...")
	p.line("Unpacking mergerfs (2.40) ...")
	p.line("Setting up fuse3 (3.14) ...")
	s := p.snapshot()
	if s.Stage != "configure" || s.Percent != 10+85*3/4 {
		t.Errorf("got stage=%q percent=%d", s.Stage, s.Percent)
	}
	p.finish(nil, "")
	if s := p.snapshot(); s.Percent != 100 || s.Running || s.Stage != "done" {
		t.Errorf("finish: %+v", s)
	}
	p.begin("samba")
	p.finish(errors.New("boom"), "E: x")
	if s := p.snapshot(); s.Stage != "failed" || s.Detail != "E: x" || len(s.Lines) != 0 {
		t.Errorf("failed: %+v", s)
	}
}
