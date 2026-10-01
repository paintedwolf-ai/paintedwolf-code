package survey

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/fileoutline"
	"github.com/lycaon/lycaon/internal/summarize"
	"github.com/lycaon/lycaon/internal/syntaxhealth"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSummarizeErrorKind(t *testing.T) {
	if got := summarizeErrorKind(nil); got != "" {
		t.Fatalf("no diags → %q want empty", got)
	}
	missing := []fileoutline.SyntaxDiagnostic{{Kind: syntaxhealth.DiagnosticMissing}}
	if got := summarizeErrorKind(missing); got != summarize.ErrorKindIncomplete {
		t.Fatalf("missing-only → %q want %q", got, summarize.ErrorKindIncomplete)
	}
	mixed := []fileoutline.SyntaxDiagnostic{{Kind: syntaxhealth.DiagnosticMissing}, {Kind: syntaxhealth.DiagnosticError}}
	if got := summarizeErrorKind(mixed); got != summarize.ErrorKindUnexpected {
		t.Fatalf("any ERROR → %q want %q", got, summarize.ErrorKindUnexpected)
	}
}

func TestBuildSummarizeResponseSurfacesFeedbackFields(t *testing.T) {
	res := summarize.Result{
		Task: "t",
		Pack: summarize.ContextPack{
			Identity: []summarize.PackIdentity{{
				Path: "a.tsx", Kind: "file", ParseHealth: "no_symbols",
				ErrorKind: summarize.ErrorKindUnexpected,
			}},
		},
		Anchors: []summarize.Anchor{
			{Path: "a.go", Line: 2, Excerpt: "func A"},
			{Path: "a.go", Line: 6, Excerpt: "helper"},
		},
		Orchestration: summarize.OrchestrationReport{
			Curator: summarize.CuratorStats{PrimaryLimitReason: "pack_budget"},
		},
	}
	resp := buildSummarizeResponse(res)

	if len(resp.Anchors) != 2 {
		t.Fatalf("anchors = %d want 2", len(resp.Anchors))
	}
	if resp.Anchors[0].Rank != 1 {
		t.Fatalf("anchor[0] rank = %d", resp.Anchors[0].Rank)
	}
	if resp.Anchors[1].Rank != 2 {
		t.Fatalf("anchor[1] rank = %d", resp.Anchors[1].Rank)
	}
	raw, err := json.Marshal(resp)
	testutil.FailErr(t, "encode briefing", err)
	for _, diagnostic := range []string{"parse_health", "errors", "parses", "error_kind"} {
		if strings.Contains(string(raw), "\""+diagnostic+"\"") {
			t.Fatalf("parser diagnostic leaked: %s", raw)
		}
	}
}
