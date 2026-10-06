package api

import (
	"testing"

	"github.com/bng147/gonas/internal/docker"
)

func TestAppProgress_AggregatesLayers(t *testing.T) {
	p := newAppProgress("portainer", "install")
	p.step("app", "pull")
	p.event("app", docker.PullEvent{ID: "latest", Status: "Pulling from portainer/portainer-ce"})
	p.event("app", docker.PullEvent{ID: "a1", Status: "Pulling fs layer"})
	p.event("app", docker.PullEvent{ID: "b2", Status: "Pulling fs layer"})
	st := p.event("app", docker.PullEvent{ID: "a1", Status: "Downloading", Current: 50, Total: 100})
	if len(st.Layers) != 2 {
		t.Fatalf("want 2 layers (the 'Pulling from' line is not a layer), got %+v", st.Layers)
	}
	if st.Downloaded != 50 || st.TotalBytes != 100 {
		t.Errorf("bytes = %d/%d, want 50/100", st.Downloaded, st.TotalBytes)
	}
	if st.Percent != 20 { // a1: 0.8*0.5=0.4, b2: 0 → avg 0.2
		t.Errorf("percent = %d, want 20", st.Percent)
	}
	p.event("app", docker.PullEvent{ID: "a1", Status: "Pull complete"})
	st = p.event("app", docker.PullEvent{ID: "b2", Status: "Already exists"})
	if st.Percent != 99 {
		t.Errorf("all layers done but not finished yet → 99, got %d", st.Percent)
	}
	st = p.finish("done", "")
	if st.Percent != 100 || st.Stage != "done" {
		t.Errorf("finish: %+v", st)
	}
	// 日誌只記狀態變化,不記每次位元組更新。
	n := len(st.Log)
	p.event("app", docker.PullEvent{ID: "a1", Status: "Pull complete"})
	if got := len(p.snapshotLocked().Log); got != n {
		t.Errorf("repeated identical status should not add log lines (%d → %d)", n, got)
	}
}
