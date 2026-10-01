package packboard_test

import (
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/packboard"
	"github.com/lycaon/lycaon/internal/standingpatterns"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestFormatFlagsLineCounts(t *testing.T) {
	line := packboard.FormatFlagsLine([]standingpatterns.FlagCount{
		{Label: "panic", Count: 3},
		{Label: "console.log", Count: 1},
	})
	if line != "Flags: panic ×3 · console.log ×1" {
		t.Fatalf("line = %q", line)
	}
}

func TestFormatFlagsLineEmptyWhenNoMatches(t *testing.T) {
	if line := packboard.FormatFlagsLine(nil); line != "" {
		t.Fatalf("line = %q", line)
	}
}

func TestFormatFlagsLineTruncatesWithMore(t *testing.T) {
	flags := make([]standingpatterns.FlagCount, 0, 12)
	for i := 0; i < 12; i++ {
		flags = append(flags, standingpatterns.FlagCount{Label: "rule-" + strings.Repeat("x", 8), Count: i + 1})
	}
	line := packboard.FormatFlagsLine(flags)
	if !strings.HasPrefix(line, "Flags: ") {
		t.Fatalf("line = %q", line)
	}
	if len(line) > packboard.MaxFlagsLineBytes {
		t.Fatalf("line len = %d exceeds %d: %q", len(line), packboard.MaxFlagsLineBytes, line)
	}
	if !strings.Contains(line, " more") {
		t.Fatalf("line = %q", line)
	}
}

func TestBuildOrientationLinesIncludesFlags(t *testing.T) {
	workers := api.BoardWorkersSlice{
		"standing_flags": []standingpatterns.FlagCount{{Label: "panic", Count: 2}},
	}
	lines := packboard.BuildOrientationLines(api.BoardSnapshot{
		Workers: &workers,
	}, packboard.OrientOpts{Now: time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC)})
	found := false
	for _, line := range lines {
		if line == "Flags: panic ×2" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("lines = %v", lines)
	}
}

func TestBuildOrientationLinesOmitsFlagsWhenAbsent(t *testing.T) {
	lines := packboard.BuildOrientationLines(api.BoardSnapshot{}, packboard.OrientOpts{
		Now: time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC),
	})
	for _, line := range lines {
		if strings.HasPrefix(line, "Flags:") {
			t.Fatalf("unexpected flags line: %q", line)
		}
	}
}

func TestBuildInjectLinesPulseIncludesFlags(t *testing.T) {
	workers := api.BoardWorkersSlice{
		"standing_flags": []standingpatterns.FlagCount{{Label: "panic", Count: 1}},
	}
	lines := packboard.BuildInjectLines(api.BoardSnapshot{
		Workers: &workers,
	}, packboard.InjectScopePulse, packboard.OrientOpts{
		Now: time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC),
	})
	found := false
	for _, line := range lines {
		if strings.HasPrefix(line, "Flags:") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("lines = %v", lines)
	}
}

func TestFormatInjectBodyRespectsBudgetWithFlags(t *testing.T) {
	now := time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC)
	flags := make([]standingpatterns.FlagCount, 0, 30)
	for i := 0; i < 30; i++ {
		flags = append(flags, standingpatterns.FlagCount{
			Label: strings.Repeat("f", 10) + string(rune('a'+i%26)),
			Count: i + 1,
		})
	}
	workers := api.BoardWorkersSlice{"standing_flags": flags}
	snap := api.BoardSnapshot{
		Repo:    api.RepoBrief{Languages: []string{"Go"}, FileCount: 100},
		Workers: &workers,
	}
	lines, _ := packboard.FormatInjectBodyScoped(snap, packboard.InjectScopeFull, false, now, api.MaxBoardInjectChars)
	body := strings.Join(lines, "\n")
	if len(body) > api.MaxBoardInjectChars {
		t.Fatalf("len=%d want <=%d body=%q", len(body), api.MaxBoardInjectChars, body)
	}
}

func TestStandingFlagsPulseSigChangesWithCounts(t *testing.T) {
	a := packboard.StandingFlagsPulseSig([]standingpatterns.FlagCount{{Label: "panic", Count: 1}})
	b := packboard.StandingFlagsPulseSig([]standingpatterns.FlagCount{{Label: "panic", Count: 2}})
	if a == b || a == "" || b == "" {
		t.Fatalf("a=%q b=%q", a, b)
	}
}
