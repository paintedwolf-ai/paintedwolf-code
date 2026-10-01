package webresearch

import (
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestParsePeriodAccepts(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"", "current"},
		{"current", "current"},
		{"CURRENT", "current"},
		{" 2025 ", "2025"},
		{"2024-2025", "2024-2025"},
		{"2025-2025", "2025"},
	}
	for _, tc := range cases {
		got, err := ParsePeriod(tc.raw)
		testutil.FailErr(t, "parse period "+tc.raw, err)
		if got.String() != tc.want {
			t.Fatalf("ParsePeriod(%q) = %q, want %q", tc.raw, got.String(), tc.want)
		}
	}
}

func TestParsePeriodRejects(t *testing.T) {
	for _, raw := range []string{"last year", "25", "20255", "2025-2024", "1970", "recent", "2025-"} {
		if got, err := ParsePeriod(raw); err == nil {
			t.Fatalf("ParsePeriod(%q) = %q, want error", raw, got.String())
		}
	}
}

func TestPeriodHistoricalOnlyForClosedPastWindows(t *testing.T) {
	now := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		raw  string
		want bool
	}{
		{"current", false},
		{"2026", false}, // the current year is not history yet
		{"2027", false},
		{"2025", true},
		{"2024-2025", true},
		{"2025-2026", false}, // window reaches the present
	}
	for _, tc := range cases {
		p, err := ParsePeriod(tc.raw)
		testutil.FailErr(t, "parse period "+tc.raw, err)
		if got := p.Historical(now); got != tc.want {
			t.Fatalf("Period(%q).Historical = %v, want %v", tc.raw, got, tc.want)
		}
	}
}

func TestPeriodIncludesOnlyDeclaredYears(t *testing.T) {
	p, err := ParsePeriod("2024-2025")
	testutil.FailErr(t, "parse period", err)
	for year, want := range map[int]bool{2023: false, 2024: true, 2025: true, 2026: false} {
		if got := p.Includes(year); got != want {
			t.Fatalf("Period(2024-2025).Includes(%d) = %v, want %v", year, got, want)
		}
	}
	// The current window ranks by recency, so it matches no year outright.
	if CurrentPeriod().Includes(2026) {
		t.Fatal("current period must not claim a year match")
	}
}

func TestProviderQueryCarriesDeclaredWindowOnly(t *testing.T) {
	if got := CurrentPeriod().providerQuery("ibm brand"); got != "ibm brand" {
		t.Fatalf("current window rewrote the provider query: %q", got)
	}
	p, err := ParsePeriod("2024-2025")
	testutil.FailErr(t, "parse period", err)
	if got := p.providerQuery("ibm brand"); got != "ibm brand 2024 2025" {
		t.Fatalf("providerQuery = %q, want the declared years appended", got)
	}
}

// Only a standalone past year is teaching material.
func TestBareYearTokenIgnoresSubjectYears(t *testing.T) {
	now := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		query string
		want  string
	}{
		{"enterprise design trends 2025", "2025"},
		{"IBM.com homepage design 2024 2025 brand", "2024"},
		{"trends (2025)", "2025"},
		{"CVE-2025-1234 mitigation", ""},
		{"Euro2024 fixtures", ""},
		{"windows 1985 release", ""}, // below the floor: a subject, not a window
		{"react hooks guide", ""},
		{"what shipped in 2026", ""}, // the current year is not a stale habit
	}
	for _, tc := range cases {
		if got := bareYearToken(tc.query, now); got != tc.want {
			t.Fatalf("bareYearToken(%q) = %q, want %q", tc.query, got, tc.want)
		}
	}
}
