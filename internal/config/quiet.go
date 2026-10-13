package config

import (
	"fmt"
	"strings"
	"time"
)

// QuietRule is a parsed quiet_hours value. The zero value is never active.
type QuietRule struct {
	days  []dayRange
	times []timeRange
}

// dayRange is an inclusive weekday range, Monday=0 .. Sunday=6; from > to
// means it wraps past Sunday (Sat-Mon).
type dayRange struct{ from, to int }

// timeRange is minutes since midnight, [from, to); from > to wraps midnight.
type timeRange struct{ from, to int }

var weekdays = map[string]int{"mon": 0, "tue": 1, "wed": 2, "thu": 3, "fri": 4, "sat": 5, "sun": 6}

// ParseQuietHours parses a comma-separated list of terms, each either a
// 24-hour window "HH:MM-HH:MM" (may wrap midnight) or a weekday "Mon" /
// range "Mon-Fri" (may wrap the week, e.g. "Sat-Mon"). The empty string is a
// valid rule that is never active.
func ParseQuietHours(s string) (QuietRule, error) {
	var r QuietRule
	if strings.TrimSpace(s) == "" {
		return r, nil
	}
	for _, term := range strings.Split(s, ",") {
		term = strings.TrimSpace(term)
		if term == "" {
			return QuietRule{}, fmt.Errorf("empty term in %q", s)
		}
		if strings.Contains(term, ":") {
			tr, err := parseTimeRange(term)
			if err != nil {
				return QuietRule{}, err
			}
			r.times = append(r.times, tr)
			continue
		}
		dr, err := parseDayRange(term)
		if err != nil {
			return QuietRule{}, err
		}
		r.days = append(r.days, dr)
	}
	return r, nil
}

func parseTimeRange(term string) (timeRange, error) {
	a, b, ok := strings.Cut(term, "-")
	if !ok {
		return timeRange{}, fmt.Errorf("bad time window %q (want HH:MM-HH:MM)", term)
	}
	from, err := parseClock(a)
	if err != nil {
		return timeRange{}, fmt.Errorf("bad time window %q: %w", term, err)
	}
	to, err := parseClock(b)
	if err != nil {
		return timeRange{}, fmt.Errorf("bad time window %q: %w", term, err)
	}
	if from == to {
		return timeRange{}, fmt.Errorf("bad time window %q: start equals end", term)
	}
	return timeRange{from, to}, nil
}

func parseClock(s string) (int, error) {
	t, err := time.Parse("15:04", strings.TrimSpace(s))
	if err != nil {
		return 0, fmt.Errorf("invalid time %q", strings.TrimSpace(s))
	}
	return t.Hour()*60 + t.Minute(), nil
}

func parseDayRange(term string) (dayRange, error) {
	a, b, hasTo := strings.Cut(term, "-")
	from, ok := weekdays[strings.ToLower(strings.TrimSpace(a))]
	if !ok {
		return dayRange{}, fmt.Errorf("unknown weekday %q in %q", strings.TrimSpace(a), term)
	}
	if !hasTo {
		return dayRange{from, from}, nil
	}
	to, ok := weekdays[strings.ToLower(strings.TrimSpace(b))]
	if !ok {
		return dayRange{}, fmt.Errorf("unknown weekday %q in %q", strings.TrimSpace(b), term)
	}
	return dayRange{from, to}, nil
}

// Active reports whether any term matches t, using t's own location.
func (r QuietRule) Active(t time.Time) bool {
	day := (int(t.Weekday()) + 6) % 7 // Monday=0
	for _, d := range r.days {
		if d.from <= d.to && day >= d.from && day <= d.to {
			return true
		}
		if d.from > d.to && (day >= d.from || day <= d.to) {
			return true
		}
	}
	m := t.Hour()*60 + t.Minute()
	for _, w := range r.times {
		if w.from < w.to && m >= w.from && m < w.to {
			return true
		}
		if w.from > w.to && (m >= w.from || m < w.to) {
			return true
		}
	}
	return false
}
