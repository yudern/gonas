package backup

import (
	"strings"
	"testing"
	"time"
)

func validJob() Job {
	return Job{
		ID:             "abc123",
		Name:           "nightly",
		SourcePath:     "/mnt/tank/media",
		DestPath:       "/mnt/backup",
		RetentionCount: 7,
		Schedule:       Schedule{EveryHours: 24, HourOfDay: 3, MinuteOfHour: 0},
	}
}

func TestJob_Validate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Job)
		wantErr bool
	}{
		{"valid", func(j *Job) {}, false},
		{"missing name", func(j *Job) { j.Name = "" }, true},
		{"blank name", func(j *Job) { j.Name = "   " }, true},
		{"relative source", func(j *Job) { j.SourcePath = "mnt/tank/media" }, true},
		{"relative dest", func(j *Job) { j.DestPath = "mnt/backup" }, true},
		{"dest equals source", func(j *Job) { j.DestPath = j.SourcePath }, true},
		{"dest inside source", func(j *Job) { j.DestPath = "/mnt/tank/media/backup" }, true},
		{"source inside dest", func(j *Job) { j.SourcePath = "/mnt/backup/media" }, true},
		{"zero retention", func(j *Job) { j.RetentionCount = 0 }, true},
		{"negative retention", func(j *Job) { j.RetentionCount = -1 }, true},
		{"invalid schedule", func(j *Job) { j.Schedule.EveryHours = 0 }, true},
		{"sibling paths are fine", func(j *Job) { j.SourcePath = "/mnt/tank/media"; j.DestPath = "/mnt/tank2/backup" }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			j := validJob()
			tt.mutate(&j)
			err := j.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestJob_Validate_TrailingSlashesDontDefeatContainmentCheck 確保
// pathContainsOrEqual 的 filepath.Clean 正規化真的有生效 —— 沒有它的話,
// "/mnt/tank/media/" 跟 "/mnt/tank/media" 這種純粹尾端斜線差異可能讓
// strings.HasPrefix 誤判,放過本來該擋下來的自我包含設定。
func TestJob_Validate_TrailingSlashesDontDefeatContainmentCheck(t *testing.T) {
	j := validJob()
	j.SourcePath = "/mnt/tank/media/"
	j.DestPath = "/mnt/tank/media/backup"
	if err := j.Validate(); err == nil {
		t.Fatal("expected trailing-slash source path to still be detected as containing the destination")
	}
}

func TestSchedule_Validate(t *testing.T) {
	tests := []struct {
		name    string
		sched   Schedule
		wantErr bool
	}{
		{"valid", Schedule{EveryHours: 24, HourOfDay: 3, MinuteOfHour: 0}, false},
		{"zero every", Schedule{EveryHours: 0, HourOfDay: 3}, true},
		{"negative every", Schedule{EveryHours: -1, HourOfDay: 3}, true},
		{"hour too low", Schedule{EveryHours: 1, HourOfDay: -1}, true},
		{"hour too high", Schedule{EveryHours: 1, HourOfDay: 24}, true},
		{"minute too low", Schedule{EveryHours: 1, MinuteOfHour: -1}, true},
		{"minute too high", Schedule{EveryHours: 1, MinuteOfHour: 60}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.sched.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestSchedule_Interval(t *testing.T) {
	s := Schedule{EveryHours: 24}
	if got, want := s.Interval().Hours(), 24.0; got != want {
		t.Errorf("Interval() = %v hours, want %v", got, want)
	}
}

// TestSchedule_EffectiveKind_EmptyKindMeansInterval is the load-bearing test
// for backward compatibility with every state.json written before Phase 15:
// those files have no "kind" field at all, which unmarshals to the zero
// value "", and that MUST keep behaving exactly like the old
// interval-only Schedule with no explicit migration code.
func TestSchedule_EffectiveKind_EmptyKindMeansInterval(t *testing.T) {
	var s Schedule
	if got := s.EffectiveKind(); got != ScheduleKindInterval {
		t.Errorf("EffectiveKind() with zero-value Kind = %q, want %q", got, ScheduleKindInterval)
	}
}

func TestSchedule_Validate_CronKind(t *testing.T) {
	tests := []struct {
		name    string
		sched   Schedule
		wantErr bool
	}{
		{"valid cron expression", Schedule{Kind: ScheduleKindCron, CronExpr: "*/5 * * * *"}, false},
		{"valid cron with ranges and lists", Schedule{Kind: ScheduleKindCron, CronExpr: "0 9-17 * * 1-5"}, false},
		{"empty cron expression", Schedule{Kind: ScheduleKindCron, CronExpr: ""}, true},
		{"blank cron expression", Schedule{Kind: ScheduleKindCron, CronExpr: "   "}, true},
		{"malformed cron expression", Schedule{Kind: ScheduleKindCron, CronExpr: "not a cron expr"}, true},
		{"cron expression with out-of-range field", Schedule{Kind: ScheduleKindCron, CronExpr: "60 * * * *"}, true},
		// A cron-kind schedule must not fall back to checking the
		// interval fields, which are irrelevant (and left at their zero
		// values, e.g. EveryHours: 0) for this kind — that must NOT be
		// mistaken for the interval-kind "everyHours must be positive"
		// error.
		{"cron kind ignores zero interval fields", Schedule{Kind: ScheduleKindCron, CronExpr: "0 3 * * *", EveryHours: 0, HourOfDay: 0, MinuteOfHour: 0}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.sched.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestSchedule_Validate_UnknownKindRejected(t *testing.T) {
	s := Schedule{Kind: "fortnightly", EveryHours: 24}
	if err := s.Validate(); err == nil {
		t.Errorf("expected error for unknown schedule kind, got nil")
	}
}

func TestSchedule_Validate_ExplicitIntervalKindStillWorks(t *testing.T) {
	// Kind explicitly set to "interval" (e.g. a Web UI form that always
	// sends the field) must behave identically to leaving it blank.
	s := Schedule{Kind: ScheduleKindInterval, EveryHours: 24, HourOfDay: 3, MinuteOfHour: 0}
	if err := s.Validate(); err != nil {
		t.Errorf("Validate() with explicit interval kind failed: %v", err)
	}
}

func TestSchedule_NextCronTime(t *testing.T) {
	s := Schedule{Kind: ScheduleKindCron, CronExpr: "30 2 * * *"}
	after := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	next, err := s.NextCronTime(after)
	if err != nil {
		t.Fatalf("NextCronTime returned error: %v", err)
	}
	want := time.Date(2026, 5, 2, 2, 30, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Errorf("NextCronTime(%v) = %v, want %v", after, next, want)
	}
}

func TestSchedule_Describe_CronKind(t *testing.T) {
	s := Schedule{Kind: ScheduleKindCron, CronExpr: "*/5 * * * *"}
	desc := s.Describe()
	if !strings.Contains(desc, "*/5 * * * *") {
		t.Errorf("Describe() = %q, expected it to contain the cron expression", desc)
	}
}

func TestJob_Validate_CronScheduleAcceptedEndToEnd(t *testing.T) {
	j := validJob()
	j.Schedule = Schedule{Kind: ScheduleKindCron, CronExpr: "0 3 * * *"}
	if err := j.Validate(); err != nil {
		t.Errorf("Job with valid cron schedule failed validation: %v", err)
	}
}
