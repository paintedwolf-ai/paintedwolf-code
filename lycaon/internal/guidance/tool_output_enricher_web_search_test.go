package guidance

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestEnricher_WebSearchRepeatCached(t *testing.T) {
	e := NewToolOutputEnricher(&HintConfig{
		HintCodes: map[string]HintEntry{
			"WEB_SEARCH_DUPLICATE_QUERY": {Message: "duplicate query — fetch_url instead"},
		},
	}, nil)
	out := e.Enrich(t.Context(), EnrichInput{
		Session: &api.Session{},
		Tool:    "web_search",
		Output:  `{"ok":true,"query":"widget","repeat_search":true,"results":[]}`,
	}).Output
	if !strings.Contains(out, "Code: WEB_SEARCH_DUPLICATE_QUERY") {
		t.Fatalf("out = %q want duplicate-query banner", out)
	}
}

// The seam writes a marker; the registry defines the words.
func TestEnricher_WebSearchPeriodHintRendersRegistryCopy(t *testing.T) {
	e := NewToolOutputEnricher(&HintConfig{
		HintCodes: map[string]HintEntry{
			"WEB_SEARCH_YEAR_IN_QUERY": {
				What: "The query carried {{ paintedwolf.year }} with no period declared.",
				Fix:  "Re-run with period={{ paintedwolf.year }} if the question is about that year.",
			},
		},
	}, nil)
	body := `{"ok":true,"query":"design trends 2025","period":"current","results":[]}`
	out := e.Enrich(t.Context(), EnrichInput{
		Session: &api.Session{},
		Tool:    "web_search",
		Output:  body + FormatPeriodHintMarker("2025"),
	}).Output

	if strings.Contains(out, MarkerPeriodHint) {
		t.Fatalf("raw marker leaked to the agent: %q", out)
	}
	for _, want := range []string{
		"Code: WEB_SEARCH_YEAR_IN_QUERY",
		"The query carried 2025 with no period declared.",
		"period=2025",
		`"query":"design trends 2025"`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("out = %q, want %q", out, want)
		}
	}
}

func TestEnricher_NoPeriodHintWithoutMarker(t *testing.T) {
	e := NewToolOutputEnricher(&HintConfig{
		HintCodes: map[string]HintEntry{"WEB_SEARCH_YEAR_IN_QUERY": {What: "x", Fix: "y"}},
	}, nil)
	out := e.Enrich(t.Context(), EnrichInput{
		Session: &api.Session{},
		Tool:    "web_search",
		Output:  `{"ok":true,"query":"CVE-2025-1234","period":"current","results":[]}`,
	}).Output
	if strings.Contains(out, "WEB_SEARCH_YEAR_IN_QUERY") {
		t.Fatalf("hint fired without a marker: %q", out)
	}
}
