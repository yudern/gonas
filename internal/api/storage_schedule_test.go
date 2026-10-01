package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bng147/gonas/internal/state"
)

func TestParitySchedule_SetPersistsAndNormalizes(t *testing.T) {
	s := newTestServer(t)
	t.Cleanup(s.stopParityScrubScheduler)

	// everyDays 0 應被正規化成 7;enabled 寫入。
	body := []byte(`{"enabled":true,"everyDays":0,"hour":3,"minute":30}`)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/storage/parity/schedule", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleStorageParityScheduleSet(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var got state.ParityScrubConfig
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if !got.Enabled || got.EveryDays != 7 || got.Hour != 3 || got.Minute != 30 {
		t.Errorf("unexpected normalized config: %+v", got)
	}
	if persisted := s.store.Snapshot().ParityScrub; persisted != got {
		t.Errorf("persisted %+v != response %+v", persisted, got)
	}
}

func TestParitySchedule_RejectsBadHour(t *testing.T) {
	s := newTestServer(t)
	t.Cleanup(s.stopParityScrubScheduler)
	body := []byte(`{"enabled":true,"everyDays":7,"hour":99,"minute":0}`)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/storage/parity/schedule", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleStorageParityScheduleSet(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an out-of-range hour, got %d", rec.Code)
	}
}
