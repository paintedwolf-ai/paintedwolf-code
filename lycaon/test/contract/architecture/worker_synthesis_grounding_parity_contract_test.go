package contract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/guidance/ledgertest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

type citationGroundingParityGolden struct {
	HadTraceableWork bool                  `json:"had_traceable_work"`
	Grounding        api.CitationGrounding `json:"grounding"`
}

func TestWorkerSynthesisGroundingParity_crossLanguageGoldens(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	fixtureDir := filepath.Join(root, "lycaon", "test", "fixtures", "citation-grounding-parity")
	cases := map[string]citationGroundingParityGolden{
		"none.json":     buildParityGoldenNone(),
		"grounded.json": buildParityGoldenGrounded(t),
		"evidence_optional.json": {
			Grounding: *guidance.EvidenceOptionalGrounding(guidance.CoordinatorCompletionReport{}),
		},
	}
	if os.Getenv("UPDATE_CITATION_GROUNDING_PARITY_GOLDENS") == "1" {
		if err := os.MkdirAll(fixtureDir, 0o755); err != nil {
			contractcheck.FailErr(t, "create directory", err)
		}
		for name, want := range cases {
			body, err := json.MarshalIndent(want, "", "  ")
			contractcheck.FailErr(t, "marshal "+name, err)
			path := filepath.Join(fixtureDir, name)
			if err := os.WriteFile(path, append(body, '\n'), 0o644); err != nil {
				contractcheck.FailErr(t, "write file", err)
			}
		}
		t.Skip("updated citation-grounding-parity goldens")
	}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(fixtureDir, name)
			body, err := os.ReadFile(path)
			testutil.FailErr(t, "read golden "+name, err)
			var got citationGroundingParityGolden
			testutil.FailErr(t, "unmarshal golden "+name, json.Unmarshal(body, &got))
			if got.HadTraceableWork != want.HadTraceableWork {
				t.Fatalf("had_traceable_work = %v want %v", got.HadTraceableWork, want.HadTraceableWork)
			}
			if !citationGroundingChecksEqual(got.Grounding.Checks, want.Grounding.Checks) {
				t.Fatalf("checks mismatch:\ngot  %+v\nwant %+v", got.Grounding.Checks, want.Grounding.Checks)
			}
		})
	}
}

func buildParityGoldenNone() citationGroundingParityGolden {
	report := guidance.CoordinatorCompletionReport{Synthesis: "Summary only"}
	ev := guidance.CloseoutEvidence{Ledger: evidence.Ledger{}}
	eval := guidance.EvaluateCloseoutCitations(evidence.CitationRoots{}, "implement_synthesis", report, ev)
	grounding := guidance.BuildCloseoutCitationGrounding(evidence.CitationRoots{}, "implement_synthesis", report, ev, eval)
	return citationGroundingParityGolden{
		HadTraceableWork: false,
		Grounding:        derefGrounding(grounding),
	}
}

func buildParityGoldenGrounded(t *testing.T) citationGroundingParityGolden {
	t.Helper()
	root := t.TempDir()
	msgs := []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "read", ID: "c1", Args: map[string]any{"path": "src/a.go"}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{
			Outcome: api.ToolResultOutcomeCompleted,
			Content: `{"path":"src/a.go","content":"1|package a","offset":1,"end_line":1}`,
		}},
	}
	ev := guidance.CloseoutEvidence{Ledger: evidence.NamespaceLedger(ledgertest.BuildFromMessages(root, msgs), "leg-a")}
	report := guidance.CoordinatorCompletionReport{
		Synthesis: "Updated handler",
		CitedEvidence: []guidance.CoordinatorCitedEvidence{{
			Path: "src/a.go", Line: 1, Excerpt: "package a",
		}},
	}
	eval := guidance.EvaluateCloseoutCitations(evidence.CitationRoots{ProjectDir: root}, "implement_synthesis", report, ev)
	grounding := guidance.BuildCloseoutCitationGrounding(evidence.CitationRoots{ProjectDir: root}, "implement_synthesis", report, ev, eval)
	return citationGroundingParityGolden{
		HadTraceableWork: true,
		Grounding:        derefGrounding(grounding),
	}
}

func derefGrounding(g *api.CitationGrounding) api.CitationGrounding {
	if g == nil {
		return api.CitationGrounding{}
	}
	return *g
}

func citationGroundingChecksEqual(a, b []api.CitationGroundingCheck) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ID != b[i].ID || a[i].Vacuous != b[i].Vacuous || a[i].Status != b[i].Status {
			return false
		}
	}
	return true
}
