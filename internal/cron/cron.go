// Package cron implements a small, dependency-free parser and scheduler for
// standard POSIX/vixie-cron style 5-field expressions:
//
//	minute hour day-of-month month day-of-week
//
// Supported syntax per field: "*", a single number, "N-M" ranges, "*/N" and
// "N-M/N" steps, and comma-separated lists combining any of the above (e.g.
// "0,15,30,45" or "1-5,10-20/2"). Month and day-of-week names (JAN, MON, ...)
// are intentionally not supported to keep the parser small; only numeric
// fields are accepted.
//
// This package exists specifically so GoNAS can offer real cron-syntax
// scheduling for backup and parity jobs without pulling in a third-party
// dependency (the project's zero-third-party-Go-dependency constraint), and
// without network access to the Go module proxy in this environment.
package cron

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Schedule is a parsed cron expression, represented as boolean membership
// sets for each of the five fields. This is the classic vixie-cron internal
// representation: cheap to evaluate with Matches, and cheap to build with a
// single shared field parser.
type Schedule struct {
	minute [60]bool
	hour   [24]bool
	dom    [32]bool // index 1..31 used
	month  [13]bool // index 1..12 used
	dow    [7]bool  // index 0..6 used, 0 = Sunday

	// domStar and dowStar record whether the original day-of-month /
	// day-of-week fields were the unrestricted "*" wildcard. This is
	// needed to implement the standard (if slightly surprising) cron
	// rule: if BOTH fields are restricted, a date matches when EITHER
	// one matches; if only one is restricted, that one alone decides.
	domStar bool
	dowStar bool

	// expr keeps the original source text for Describe/debugging.
	expr string
}

type fieldSpec struct {
	name string
	min  int
	max  int
}

var (
	minuteSpec = fieldSpec{"minute", 0, 59}
	hourSpec   = fieldSpec{"hour", 0, 23}
	domSpec    = fieldSpec{"day-of-month", 1, 31}
	monthSpec  = fieldSpec{"month", 1, 12}
	dowSpec    = fieldSpec{"day-of-week", 0, 7} // 7 is accepted as an alias for Sunday
)

// Parse parses a standard 5-field cron expression into a Schedule.
func Parse(expr string) (Schedule, error) {
	original := expr
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return Schedule{}, fmt.Errorf("cron: expected 5 fields (minute hour dom month dow), got %d in %q", len(fields), original)
	}

	var sched Schedule
	sched.expr = original

	minuteSet, err := parseField(fields[0], minuteSpec)
	if err != nil {
		return Schedule{}, err
	}
	for _, v := range minuteSet {
		sched.minute[v] = true
	}

	hourSet, err := parseField(fields[1], hourSpec)
	if err != nil {
		return Schedule{}, err
	}
	for _, v := range hourSet {
		sched.hour[v] = true
	}

	domField := fields[2]
	sched.domStar = domField == "*"
	domSet, err := parseField(domField, domSpec)
	if err != nil {
		return Schedule{}, err
	}
	for _, v := range domSet {
		sched.dom[v] = true
	}

	monthSet, err := parseField(fields[3], monthSpec)
	if err != nil {
		return Schedule{}, err
	}
	for _, v := range monthSet {
		sched.month[v] = true
	}

	dowField := fields[4]
	sched.dowStar = dowField == "*"
	dowSet, err := parseField(dowField, dowSpec)
	if err != nil {
		return Schedule{}, err
	}
	for _, v := range dowSet {
		sched.dow[v%7] = true // fold 7 -> 0 (Sunday)
	}

	return sched, nil
}

// parseField parses a single comma-separated cron field into the list of
// integer values it selects, validating each against spec's [min, max]
// bounds.
func parseField(field string, spec fieldSpec) ([]int, error) {
	var values []int
	for _, part := range strings.Split(field, ",") {
		if part == "" {
			return nil, fmt.Errorf("cron: empty entry in %s field %q", spec.name, field)
		}

		rangePart := part
		step := 1
		if idx := strings.Index(part, "/"); idx >= 0 {
			rangePart = part[:idx]
			stepStr := part[idx+1:]
			n, err := strconv.Atoi(stepStr)
			if err != nil || n <= 0 {
				return nil, fmt.Errorf("cron: invalid step %q in %s field %q", stepStr, spec.name, field)
			}
			step = n
		}

		var lo, hi int
		switch {
		case rangePart == "*":
			lo, hi = spec.min, spec.max
		case strings.Contains(rangePart, "-"):
			bounds := strings.SplitN(rangePart, "-", 2)
			a, errA := strconv.Atoi(bounds[0])
			b, errB := strconv.Atoi(bounds[1])
			if errA != nil || errB != nil {
				return nil, fmt.Errorf("cron: invalid range %q in %s field %q", rangePart, spec.name, field)
			}
			lo, hi = a, b
			if lo > hi {
				return nil, fmt.Errorf("cron: range start %d greater than end %d in %s field %q", lo, hi, spec.name, field)
			}
		default:
			n, err := strconv.Atoi(rangePart)
			if err != nil {
				return nil, fmt.Errorf("cron: invalid value %q in %s field %q (numeric fields only, names like JAN/MON are not supported)", rangePart, spec.name, field)
			}
			lo, hi = n, n
		}

		if lo < spec.min || hi > spec.max {
			return nil, fmt.Errorf("cron: %s value out of range [%d,%d] in field %q", spec.name, spec.min, spec.max, field)
		}

		for v := lo; v <= hi; v += step {
			values = append(values, v)
		}
	}
	return values, nil
}

// Matches reports whether t falls within this schedule, applying the
// standard cron day-of-month/day-of-week OR-logic: if both fields are
// restricted (neither is "*"), a time matches if EITHER field matches; if
// only one is restricted, only that one is consulted. Seconds/nanoseconds
// are ignored.
func (s Schedule) Matches(t time.Time) bool {
	if !s.minute[t.Minute()] {
		return false
	}
	if !s.hour[t.Hour()] {
		return false
	}
	if !s.month[int(t.Month())] {
		return false
	}

	domMatch := s.dom[t.Day()]
	dowMatch := s.dow[int(t.Weekday())]

	switch {
	case s.domStar && s.dowStar:
		return true
	case s.domStar:
		return dowMatch
	case s.dowStar:
		return domMatch
	default:
		return domMatch || dowMatch
	}
}

// maxSearchWindow bounds Next's brute-force search so that an expression
// which can never match (e.g. "0 0 30 2 *", the 30th of February) fails
// fast with an error instead of looping forever.
const maxSearchWindow = 4 * 366 * 24 * time.Hour

// Next returns the next time strictly after `after` that matches the
// schedule, searching minute-by-minute up to roughly 4 years out. This
// brute-force approach is intentionally simple (no calendar-arithmetic
// shortcuts) since it only needs to run once per scheduled job execution,
// not in a hot loop, and it must be correct for every edge case (month
// rollover, leap years, impossible dom/month combinations) with minimal
// code.
func (s Schedule) Next(after time.Time) (time.Time, error) {
	// Start at the next whole minute after `after`, since cron granularity
	// is one minute and Matches ignores sub-minute precision.
	loc := after.Location()
	t := time.Date(after.Year(), after.Month(), after.Day(), after.Hour(), after.Minute(), 0, 0, loc).Add(time.Minute)

	deadline := after.Add(maxSearchWindow)
	for t.Before(deadline) {
		if s.Matches(t) {
			return t, nil
		}
		t = t.Add(time.Minute)
	}
	return time.Time{}, fmt.Errorf("cron: no matching time found for expression %q within %s of %s (check for an impossible date, e.g. day 30 of February)", s.expr, maxSearchWindow, after)
}

// String returns the original expression text this Schedule was parsed
// from.
func (s Schedule) String() string {
	return s.expr
}
