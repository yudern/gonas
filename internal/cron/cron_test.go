package cron

import (
	"strings"
	"testing"
	"time"
)

func mustParse(t *testing.T, expr string) Schedule {
	t.Helper()
	sched, err := Parse(expr)
	if err != nil {
		t.Fatalf("Parse(%q) failed: %v", expr, err)
	}
	return sched
}

func TestParse_InvalidFieldCount(t *testing.T) {
	cases := []string{
		"",
		"* * *",
		"* * * * * *",
		"*",
	}
	for _, expr := range cases {
		if _, err := Parse(expr); err == nil {
			t.Errorf("Parse(%q) expected error, got nil", expr)
		}
	}
}

func TestParse_OutOfRangeValues(t *testing.T) {
	cases := []string{
		"60 * * * *", // minute max 59
		"* 24 * * *", // hour max 23
		"* * 32 * *", // dom max 31
		"* * 0 * *",  // dom min 1
		"* * * 13 *", // month max 12
		"* * * 0 *",  // month min 1
		"* * * * 8",  // dow max 7
		"-1 * * * *", // negative
	}
	for _, expr := range cases {
		if _, err := Parse(expr); err == nil {
			t.Errorf("Parse(%q) expected out-of-range error, got nil", expr)
		}
	}
}

func TestParse_RejectsMonthAndDayNames(t *testing.T) {
	cases := []string{
		"0 0 * JAN *",
		"0 0 * * MON",
	}
	for _, expr := range cases {
		if _, err := Parse(expr); err == nil {
			t.Errorf("Parse(%q) expected error (names unsupported), got nil", expr)
		}
	}
}

func TestParse_InvalidRangeOrder(t *testing.T) {
	if _, err := Parse("30-10 * * * *"); err == nil {
		t.Errorf("expected error for descending range 30-10")
	}
}

func TestSchedule_Matches_EveryMinute(t *testing.T) {
	sched := mustParse(t, "* * * * *")
	tm := time.Date(2026, 3, 15, 13, 45, 0, 0, time.UTC)
	if !sched.Matches(tm) {
		t.Errorf("expected '* * * * *' to match every minute, got no match for %v", tm)
	}
}

func TestSchedule_Matches_StepSyntax(t *testing.T) {
	sched := mustParse(t, "*/15 * * * *")
	for _, minute := range []int{0, 15, 30, 45} {
		tm := time.Date(2026, 1, 1, 0, minute, 0, 0, time.UTC)
		if !sched.Matches(tm) {
			t.Errorf("expected minute %d to match */15, got no match", minute)
		}
	}
	for _, minute := range []int{1, 14, 16, 44, 59} {
		tm := time.Date(2026, 1, 1, 0, minute, 0, 0, time.UTC)
		if sched.Matches(tm) {
			t.Errorf("expected minute %d NOT to match */15, got match", minute)
		}
	}
}

func TestSchedule_Matches_RangeAndList(t *testing.T) {
	sched := mustParse(t, "0 9-17 * * 1,3,5")
	// Wednesday (3) at 12:00 should match (in range 9-17, weekday in list).
	wed := time.Date(2026, 3, 4, 12, 0, 0, 0, time.UTC) // 2026-03-04 is a Wednesday
	if wed.Weekday() != time.Wednesday {
		t.Fatalf("test setup error: expected Wednesday, got %v", wed.Weekday())
	}
	if !sched.Matches(wed) {
		t.Errorf("expected Wednesday 12:00 to match, got no match")
	}
	// Tuesday should not match (2 not in 1,3,5).
	tue := wed.AddDate(0, 0, -1)
	if sched.Matches(tue) {
		t.Errorf("expected Tuesday not to match, got match")
	}
	// 08:00 on a matching weekday should not match (hour out of range).
	early := time.Date(2026, 3, 4, 8, 0, 0, 0, time.UTC)
	if sched.Matches(early) {
		t.Errorf("expected 08:00 not to match hour range 9-17, got match")
	}
}

func TestSchedule_Matches_RangeWithStep(t *testing.T) {
	sched := mustParse(t, "0-30/10 * * * *")
	for _, minute := range []int{0, 10, 20, 30} {
		tm := time.Date(2026, 1, 1, 0, minute, 0, 0, time.UTC)
		if !sched.Matches(tm) {
			t.Errorf("expected minute %d to match 0-30/10, got no match", minute)
		}
	}
	for _, minute := range []int{5, 31, 40} {
		tm := time.Date(2026, 1, 1, 0, minute, 0, 0, time.UTC)
		if sched.Matches(tm) {
			t.Errorf("expected minute %d NOT to match 0-30/10, got match", minute)
		}
	}
}

// TestSchedule_Matches_DomDowOrLogic verifies the classic (and often
// surprising) POSIX cron rule: when BOTH day-of-month and day-of-week are
// restricted (neither is "*"), a date matches if EITHER field matches, not
// both.
func TestSchedule_Matches_DomDowOrLogic(t *testing.T) {
	// "the 1st of the month OR any Monday"
	sched := mustParse(t, "0 0 1 * 1")

	// 2026-03-01 is a Sunday (dom matches, dow does not) -> should match via dom.
	firstOfMonth := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	if firstOfMonth.Weekday() != time.Sunday {
		t.Fatalf("test setup error: expected Sunday, got %v", firstOfMonth.Weekday())
	}
	if !sched.Matches(firstOfMonth) {
		t.Errorf("expected 1st of month (via dom OR-match) to match, got no match")
	}

	// 2026-03-02 is a Monday but not the 1st -> should match via dow.
	monday := time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)
	if monday.Weekday() != time.Monday {
		t.Fatalf("test setup error: expected Monday, got %v", monday.Weekday())
	}
	if !sched.Matches(monday) {
		t.Errorf("expected Monday (via dow OR-match) to match, got no match")
	}

	// 2026-03-03 is a Tuesday and not the 1st -> should NOT match.
	tuesday := time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC)
	if sched.Matches(tuesday) {
		t.Errorf("expected non-1st non-Monday to NOT match, got match")
	}
}

func TestSchedule_Matches_DomOnlyRestricted(t *testing.T) {
	// dow is "*" (unrestricted) so only dom is consulted.
	sched := mustParse(t, "0 0 15 * *")
	fifteenth := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)
	if !sched.Matches(fifteenth) {
		t.Errorf("expected 15th to match when dow is '*', got no match")
	}
	other := time.Date(2026, 6, 16, 0, 0, 0, 0, time.UTC)
	if sched.Matches(other) {
		t.Errorf("expected 16th NOT to match, got match")
	}
}

func TestSchedule_Matches_DowSevenAliasesSunday(t *testing.T) {
	sched := mustParse(t, "0 0 * * 7")
	sunday := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	if sunday.Weekday() != time.Sunday {
		t.Fatalf("test setup error: expected Sunday")
	}
	if !sched.Matches(sunday) {
		t.Errorf("expected dow=7 to alias Sunday, got no match")
	}
}

func TestSchedule_Next_SimpleEveryMinute(t *testing.T) {
	sched := mustParse(t, "* * * * *")
	after := time.Date(2026, 5, 1, 10, 30, 20, 0, time.UTC) // has seconds, mid-minute
	next, err := sched.Next(after)
	if err != nil {
		t.Fatalf("Next returned error: %v", err)
	}
	want := time.Date(2026, 5, 1, 10, 31, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Errorf("Next(%v) = %v, want %v", after, next, want)
	}
}

func TestSchedule_Next_DailyAtFixedTime(t *testing.T) {
	sched := mustParse(t, "30 2 * * *") // 02:30 every day
	after := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	next, err := sched.Next(after)
	if err != nil {
		t.Fatalf("Next returned error: %v", err)
	}
	want := time.Date(2026, 5, 2, 2, 30, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Errorf("Next(%v) = %v, want %v", after, next, want)
	}
}

func TestSchedule_Next_MonthRollover(t *testing.T) {
	sched := mustParse(t, "0 0 1 * *") // midnight on the 1st of every month
	after := time.Date(2026, 1, 31, 23, 0, 0, 0, time.UTC)
	next, err := sched.Next(after)
	if err != nil {
		t.Fatalf("Next returned error: %v", err)
	}
	want := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Errorf("Next(%v) = %v, want %v", after, next, want)
	}
}

func TestSchedule_Next_LeapYearFeb29(t *testing.T) {
	sched := mustParse(t, "0 12 29 2 *") // Feb 29 at noon (leap years only)
	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	next, err := sched.Next(after)
	if err != nil {
		t.Fatalf("Next returned error: %v", err)
	}
	// 2028 is the next leap year after 2026.
	want := time.Date(2028, 2, 29, 12, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Errorf("Next(%v) = %v, want %v", after, next, want)
	}
}

func TestSchedule_Next_ImpossibleDateErrors(t *testing.T) {
	sched := mustParse(t, "0 0 30 2 *") // February 30th never exists
	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	_, err := sched.Next(after)
	if err == nil {
		t.Fatalf("expected error for impossible date (Feb 30), got nil")
	}
	if !strings.Contains(err.Error(), "no matching time found") {
		t.Errorf("expected descriptive error, got: %v", err)
	}
}

func TestSchedule_String_ReturnsOriginalExpression(t *testing.T) {
	sched := mustParse(t, "*/5 8-18 * * 1-5")
	if got := sched.String(); got != "*/5 8-18 * * 1-5" {
		t.Errorf("String() = %q, want original expression", got)
	}
}
