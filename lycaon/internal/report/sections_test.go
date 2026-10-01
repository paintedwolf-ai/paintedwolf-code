package report

import (
	"strings"
	"testing"
)

// Levels that scored nothing are dropped.
func TestLevelCounts_OrdersBySeverityAndDropsZeroes(t *testing.T) {
	got := levelCounts(&ReportScan{ByLevel: map[string]int{
		"info": 145, "critical": 0, "high": 1, "low": 0, "medium": 2,
	}})

	var order []string
	for _, lc := range got {
		order = append(order, lc.level)
	}
	if strings.Join(order, ",") != "high,medium,info" {
		t.Fatalf("level order = %v, want high,medium,info with the zeroes dropped", order)
	}
}

// Severity tone keys on rank, not spelling, so a scanner that grades
// "warning" takes the same tone as one that grades "medium" — and a word the
// renderer has never ranked takes no tone at all.
func TestSeverityTone_ByRankNotSpelling(t *testing.T) {
	if severityTone("warning") != toneMedium || severityTone("MEDIUM") != toneMedium {
		t.Fatal("warning and medium rank the same and must share a tone")
	}
	if severityTone("note") != toneInfo || severityTone("error") != toneHigh {
		t.Fatal("note and error must map to the info and high tones")
	}
	if severityTone("blocker") != toneNeutral {
		t.Fatal("an unranked word must take the neutral tone rather than a guess")
	}
}

// A level's declared tone picks its colours; a tone the renderer has never
// seen takes plain ink rather than a guess.
func TestBriefTone_DeclaredOrInk(t *testing.T) {
	if briefTone("good") != toneGood || briefTone("critical") != toneCritical {
		t.Fatal("declared tones must map to their colours")
	}
	if briefTone("") != toneInk || briefTone("teal") != toneInk {
		t.Fatal("an undeclared tone must take plain ink")
	}
}

// A workflow names its own verdict members, so the renderer writes whatever it
// is given.
func TestHumanize(t *testing.T) {
	cases := map[string]string{
		"threat_model":       "Threat model",
		"accepted_residuals": "Accepted residuals",
		"winner":             "Winner",
		"coverage-gaps":      "Coverage gaps",
		"":                   "",
	}
	for in, want := range cases {
		if got := humanize(in); got != want {
			t.Fatalf("humanize(%q) = %q, want %q", in, got, want)
		}
	}
	if got := chipText("NEEDS_REVISION"); got != "Needs revision" {
		t.Fatalf("chipText = %q, want sentence case", got)
	}
}
