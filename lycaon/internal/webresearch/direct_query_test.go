package webresearch

import (
	"strings"
	"testing"
)

func TestParseQueryOperatorsSitePin(t *testing.T) {
	ops := parseQueryOperators("site:store.steampowered.com Steam Machine specs")
	if len(ops.siteBases) != 1 || ops.siteBases[0] != "https://store.steampowered.com" {
		t.Fatalf("siteBases = %+v", ops.siteBases)
	}
	if ops.cleaned != "Steam Machine specs" {
		t.Fatalf("cleaned = %q", ops.cleaned)
	}
}

func TestParseQueryOperatorsExplicitScheme(t *testing.T) {
	ops := parseQueryOperators("site:http://127.0.0.1:8080 widget docs")
	if len(ops.siteBases) != 1 || ops.siteBases[0] != "http://127.0.0.1:8080" {
		t.Fatalf("siteBases = %+v", ops.siteBases)
	}
	if ops.cleaned != "widget docs" {
		t.Fatalf("cleaned = %q", ops.cleaned)
	}
}

func TestParseQueryOperatorsInvalidSiteStaysText(t *testing.T) {
	ops := parseQueryOperators("site:localhost widget docs")
	if len(ops.siteBases) != 0 {
		t.Fatalf("siteBases = %+v want none for dotless host", ops.siteBases)
	}
	if !strings.Contains(ops.cleaned, "site:localhost") {
		t.Fatalf("cleaned = %q want invalid operator kept as text", ops.cleaned)
	}
}

func TestParseQueryOperatorsNoOperators(t *testing.T) {
	ops := parseQueryOperators(`"Steam Machine" 2026 quiet`)
	if len(ops.siteBases) != 0 {
		t.Fatalf("siteBases = %+v", ops.siteBases)
	}
	if ops.cleaned != `"Steam Machine" 2026 quiet` {
		t.Fatalf("cleaned = %q", ops.cleaned)
	}
}

func TestQuotedPhrasesFeedScorer(t *testing.T) {
	scorer := newQueryScorer(`"steam machine" review 2026`, nil, false, CurrentPeriod())
	found := false
	for _, p := range scorer.phrases {
		if p == "steam machine" {
			found = true
		}
	}
	if !found {
		t.Fatalf("phrases = %+v want quoted phrase", scorer.phrases)
	}
	// The quoted span outscores the same tokens scattered across the page.
	phrased := scorer.scoreProbe(pageProbe{textSample: "the steam machine review"})
	scattered := scorer.scoreProbe(pageProbe{textSample: "steam powers this machine review"})
	if phrased <= scattered {
		t.Fatalf("phrased %v should beat scattered %v", phrased, scattered)
	}
}
