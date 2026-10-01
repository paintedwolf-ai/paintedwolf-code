package guidance

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestOutputLooksLikePipedTUI_altScreen(t *testing.T) {
	if !outputLooksLikePipedTUI("prefix\x1b[?1049hTITLE") {
		t.Fatal("expected alt-screen marker to trigger")
	}
	if outputLooksLikePipedTUI("plain stdout with no escapes") {
		t.Fatal("plain stdout must not trigger")
	}
	// Color CSI alone is not a full-screen TUI signal.
	if outputLooksLikePipedTUI("\x1b[31mred\x1b[0m") {
		t.Fatal("SGR color alone must not trigger")
	}
}

func TestAppendTUINotDrivenBanner(t *testing.T) {
	e := NewToolOutputEnricher(&HintConfig{HintCodes: map[string]HintEntry{
		"TUI_NOT_DRIVEN": {Message: "drive with terminal_open"},
	}}, nil)
	first := e.Enrich(t.Context(), EnrichInput{
		SessionID: "s1",
		Session:   &api.Session{ID: "s1"},
		Tool:      "command",
		Output:    "garbage\x1b[?1049h\x1b[Hmenu",
	})
	out := first.Output
	if !strings.Contains(out, "Code: TUI_NOT_DRIVEN") {
		t.Fatalf("output missing TUI_NOT_DRIVEN banner:\n%s", out)
	}
	if !strings.Contains(out, "drive with terminal_open") {
		t.Fatalf("output missing hint message:\n%s", out)
	}
	// Re-enriching carries the facts already raised, so the banner is not appended twice.
	again := e.Enrich(t.Context(), EnrichInput{
		SessionID: "s1",
		Session:   &api.Session{ID: "s1"},
		Tool:      "command",
		Output:    out,
		Facts:     first.Facts,
	}).Output
	if got := strings.Count(again, "Code: TUI_NOT_DRIVEN"); got != 1 {
		t.Fatalf("want one Code line, got %d:\n%s", got, again)
	}
}
