package webresearch

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// periodCurrent is the wire spelling of the default window.
const periodCurrent = "current"

// Declared windows are bounded: below the floor a four-digit number is a
// subject name ("Windows 2000"), not a research window; above the ceiling it is
// a typo the pipeline cannot rank for.
const (
	periodFloorYear   = 1990
	periodCeilingYear = 2999
)

// Period is the time window a search is about — the `period` argument of
// web_search, and the only control signal for time. A year inside the query
// string is ordinary text that steers nothing, which keeps subject years
// ("CVE-2025-1234") searchable. The zero value is the current window.
type Period struct {
	// from and to are inclusive years. Both zero means the current window.
	from int
	to   int
}

// CurrentPeriod is the default window: today's state, recency ranked.
func CurrentPeriod() Period { return Period{} }

// ParsePeriod reads the wire value: "", "current", "YYYY", or "YYYY-YYYY".
func ParsePeriod(raw string) (Period, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" || s == periodCurrent {
		return Period{}, nil
	}
	lo, hi, ranged := strings.Cut(s, "-")
	from, err := parsePeriodYear(lo)
	if err != nil {
		return Period{}, err
	}
	to := from
	if ranged {
		if to, err = parsePeriodYear(hi); err != nil {
			return Period{}, err
		}
	}
	if to < from {
		return Period{}, fmt.Errorf("period %q ends before it starts", raw)
	}
	return Period{from: from, to: to}, nil
}

func parsePeriodYear(raw string) (int, error) {
	s := strings.TrimSpace(raw)
	if len(s) != 4 {
		return 0, fmt.Errorf("period year %q is not four digits", raw)
	}
	year, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("period year %q is not a number", raw)
	}
	if year < periodFloorYear || year > periodCeilingYear {
		return 0, fmt.Errorf("period year %q is outside %d-%d", raw, periodFloorYear, periodCeilingYear)
	}
	return year, nil
}

// IsCurrent reports the default window — no year was declared.
func (p Period) IsCurrent() bool { return p.from == 0 }

// Historical reports a declared window that closed before the current year.
// The pages such a search wants are, by construction, the older ones.
func (p Period) Historical(now time.Time) bool {
	return !p.IsCurrent() && p.to < now.Year()
}

// Includes reports whether a page's year falls inside the declared window.
// The current window includes nothing: it ranks by recency, not by year match.
func (p Period) Includes(year int) bool {
	if p.IsCurrent() {
		return false
	}
	return year >= p.from && year <= p.to
}

// Newest is the latest year in the window, or 0 for the current window.
func (p Period) Newest() int {
	if p.IsCurrent() {
		return 0
	}
	return p.to
}

// String is the wire spelling, round-tripping through ParsePeriod.
func (p Period) String() string {
	if p.IsCurrent() {
		return periodCurrent
	}
	if p.from == p.to {
		return strconv.Itoa(p.from)
	}
	return strconv.Itoa(p.from) + "-" + strconv.Itoa(p.to)
}

// years lists the window's years, oldest first. Empty for the current window.
func (p Period) years() []string {
	if p.IsCurrent() {
		return nil
	}
	out := make([]string, 0, p.to-p.from+1)
	for y := p.from; y <= p.to; y++ {
		out = append(out, strconv.Itoa(y))
	}
	return out
}

// bareYearToken returns the first standalone past-year token in a query, or "".
// Standalone means the whole token is the year, so subject years inside a larger
// token ("CVE-2025-1234") do not match. Drives teaching copy only — nothing in
// ranking reads it.
func bareYearToken(query string, now time.Time) string {
	for _, field := range strings.Fields(query) {
		tok := strings.Trim(field, `"'.,:;!?()[]{}`)
		if len(tok) != 4 {
			continue
		}
		year, err := strconv.Atoi(tok)
		if err != nil {
			continue
		}
		if year >= periodFloorYear && year < now.Year() {
			return tok
		}
	}
	return ""
}

// providerQuery is the string handed to catalog providers. They take a query
// and nothing else, so a declared window has to ride in the text.
func (p Period) providerQuery(query string) string {
	if p.IsCurrent() {
		return query
	}
	return strings.TrimSpace(query + " " + strings.Join(p.years(), " "))
}
