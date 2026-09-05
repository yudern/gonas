package backup

import "testing"

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
