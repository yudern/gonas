package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bng147/gonas/internal/state"
	"github.com/bng147/gonas/internal/storage"
)

// smartSelfTestRunner 記錄 smartctl -t 的呼叫,並對 -l selftest 回固定紀錄。
type smartSelfTestRunner struct {
	startCalls [][]string
	failOn     map[string]bool // device -> 觸發 -t 時是否失敗
}

func (r *smartSelfTestRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	if name != "smartctl" {
		return nil, errors.New("unexpected command: " + name)
	}
	if len(args) >= 2 && args[0] == "-t" {
		device := args[len(args)-1]
		r.startCalls = append(r.startCalls, append([]string{}, args...))
		if r.failOn[device] {
			return nil, errors.New("smartctl: device does not support self-tests")
		}
		return []byte("Testing has begun."), nil
	}
	if len(args) >= 2 && args[0] == "-l" && args[1] == "selftest" {
		return []byte("# 1  Short offline       Completed without error       00%      100         -\n"), nil
	}
	return nil, errors.New("unexpected smartctl args")
}

func (r *smartSelfTestRunner) RunWithStdin(_ context.Context, _ []byte, _ string, _ ...string) ([]byte, error) {
	return nil, errors.New("unexpected RunWithStdin")
}

func seedPool(t *testing.T, s *Server) {
	t.Helper()
	if err := s.store.Update(func(st *state.State) error {
		st.Pool = &storage.PoolConfig{
			Name:        "tank",
			MountPoint:  "/mnt/tank",
			DataDisks:   []string{"/dev/sda", "/dev/sdb"},
			ParityDisks: []string{"/dev/sdc"},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSmartScheduleSet_ValidatesAndPersists(t *testing.T) {
	s := newTestServer(t)
	defer s.stopSmartTestScheduler()

	body, _ := json.Marshal(state.SmartTestConfig{Enabled: true, EveryDays: 7, Hour: 4, Minute: 0, Kind: "long"})
	req := httptest.NewRequest("PUT", "/api/v1/storage/smart/schedule", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleStorageSmartScheduleSet(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	got := s.store.Snapshot().SmartTest
	if !got.Enabled || got.Kind != "long" || got.Hour != 4 {
		t.Errorf("config not persisted as expected: %+v", got)
	}
}

func TestSmartScheduleSet_BadHourRejected(t *testing.T) {
	s := newTestServer(t)
	body, _ := json.Marshal(state.SmartTestConfig{Enabled: true, EveryDays: 7, Hour: 99, Minute: 0, Kind: "short"})
	req := httptest.NewRequest("PUT", "/api/v1/storage/smart/schedule", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleStorageSmartScheduleSet(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for hour out of range, got %d", rec.Code)
	}
}

func TestSmartScheduleSet_InvalidKindNormalizedToShort(t *testing.T) {
	s := newTestServer(t)
	defer s.stopSmartTestScheduler()
	body, _ := json.Marshal(state.SmartTestConfig{Enabled: true, EveryDays: 1, Hour: 2, Minute: 0, Kind: "medium"})
	req := httptest.NewRequest("PUT", "/api/v1/storage/smart/schedule", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleStorageSmartScheduleSet(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if k := s.store.Snapshot().SmartTest.Kind; k != "short" {
		t.Errorf("expected invalid kind normalized to short, got %q", k)
	}
}

func TestSmartTestRun_TriggersEachPoolDisk(t *testing.T) {
	s := newTestServer(t)
	seedPool(t, s)
	runner := &smartSelfTestRunner{failOn: map[string]bool{"/dev/sdc": true}}
	s.runner = runner

	body, _ := json.Marshal(smartTestRunRequest{Kind: "short"})
	req := httptest.NewRequest("POST", "/api/v1/storage/smart/test", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleStorageSmartTestRun(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}
	// 三顆碟都被觸發一次 -t。
	if len(runner.startCalls) != 3 {
		t.Errorf("expected 3 smartctl -t calls (one per disk), got %d", len(runner.startCalls))
	}
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if started, _ := resp["started"].(float64); started != 2 {
		t.Errorf("expected 2 started (sdc fails), got %v", resp["started"])
	}
}

func TestSmartTestRun_NoPoolRejected(t *testing.T) {
	s := newTestServer(t)
	s.runner = &smartSelfTestRunner{}
	body, _ := json.Marshal(smartTestRunRequest{Kind: "short"})
	req := httptest.NewRequest("POST", "/api/v1/storage/smart/test", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleStorageSmartTestRun(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 when no pool configured, got %d", rec.Code)
	}
}

func TestSmartSelfTestLog_RejectsUnknownDevice(t *testing.T) {
	s := newTestServer(t)
	seedPool(t, s)
	s.runner = &smartSelfTestRunner{}

	// 不在 pool 裡的裝置要被擋。
	req := httptest.NewRequest("GET", "/api/v1/storage/smart/selftest-log?device=/dev/evil", nil)
	rec := httptest.NewRecorder()
	s.handleStorageSmartSelfTestLog(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a device outside the pool, got %d", rec.Code)
	}

	// pool 裡的裝置正常回紀錄。
	req2 := httptest.NewRequest("GET", "/api/v1/storage/smart/selftest-log?device=/dev/sda", nil)
	rec2 := httptest.NewRecorder()
	s.handleStorageSmartSelfTestLog(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200 for a pool device, got %d: %s", rec2.Code, rec2.Body.String())
	}
	var entries []storage.SmartSelfTestEntry
	if err := json.Unmarshal(rec2.Body.Bytes(), &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || !entries[0].Passed {
		t.Errorf("unexpected self-test log entries: %+v", entries)
	}
}
