package guidance_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/guidance/ledgertest"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/verification"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestBuildCloseoutCitationGrounding_citedEvidence(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	readJSON := `{"path":"src/a.go","content":"1|package a\n","offset":1,"end_line":1}`
	msgs := []api.Message{
		{
			Role:      api.MessageRoleAssistant,
			ToolCalls: []api.ToolCall{{Name: "read", ID: "r1", Args: map[string]any{"path": "src/a.go"}}},
		},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{
			Outcome: api.ToolResultOutcomeCompleted,
			Content: readJSON,
		}},
	}
	ev := guidance.CloseoutEvidence{Ledger: ledgertest.BuildFromMessages(root, msgs)}
	report := guidance.CoordinatorCompletionReport{
		Synthesis: "Summary.",
		CitedEvidence: []guidance.CoordinatorCitedEvidence{{
			Path: "src/a.go", Line: 1, Excerpt: "package a",
		}},
	}
	out := guidance.BuildCloseoutCitationGrounding(evidence.CitationRoots{ProjectDir: root}, spawn.SurfaceImplementSynthesis, report, ev, guidance.CloseoutGroundingEval{})
	if out == nil {
		t.Fatal("expected grounding")
	}
	if len(out.CitedEvidence) != 1 {
		t.Fatalf("cited_evidence = %+v want 1 entry", out.CitedEvidence)
	}
	if out.CitedEvidence[0].Path != "src/a.go" {
		t.Fatalf("path = %q", out.CitedEvidence[0].Path)
	}
}

func TestBuildCloseoutCitationGroundingHandleOnlyCountMatchesResolvedEvidence(t *testing.T) {
	t.Parallel()

	msgs := []api.Message{
		{
			Role: api.MessageRoleAssistant,
			ToolCalls: []api.ToolCall{{
				Name: "secret_revoke", ID: "revoke-1",
				Args: map[string]any{"reference": "{{paintedwolf-secret:00000000-0000-0000-0000-000000000000}}"},
			}},
		},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{
			Outcome: api.ToolResultOutcomeCompleted,
			Content: `{"revoked":true,"value_disclosed":false}`,
		}},
	}
	ev := guidance.CloseoutEvidence{Ledger: ledgertest.BuildFromMessages("", msgs)}
	report := guidance.CoordinatorCompletionReport{
		Synthesis: "The test reference was revoked.",
		CitedEvidence: []guidance.CoordinatorCitedEvidence{{
			Evidence: "secret_lifecycle#1",
		}},
	}
	out := guidance.BuildCloseoutCitationGrounding(evidence.CitationRoots{}, spawn.SurfaceImplementSynthesis, report, ev, guidance.CloseoutGroundingEval{})
	if out == nil || len(out.CitedEvidence) != 1 {
		t.Fatalf("grounding = %+v want one resolved handle", out)
	}
	for _, check := range out.Checks {
		if check.ID != "cited_evidence" {
			continue
		}
		if check.Summary != "1 citation(s) matched closeout evidence ledger" || len(check.Matched) != 1 {
			t.Fatalf("check = %+v want count and matched handle to agree", check)
		}
		return
	}
	t.Fatal("missing cited_evidence check")
}

// A cited regular file under the project dir reaches the wire DTO as openable.
func TestBuildCloseoutCitationGrounding_openableFile(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	testutil.FailErr(t, "MkdirAll src", os.MkdirAll(filepath.Join(root, "src"), 0o755))
	testutil.FailErr(t, "WriteFile a.go", os.WriteFile(filepath.Join(root, "src", "a.go"), []byte("package a\n"), 0o644))

	readJSON := `{"path":"src/a.go","content":"1|package a\n","offset":1,"end_line":1}`
	msgs := []api.Message{
		{
			Role:      api.MessageRoleAssistant,
			ToolCalls: []api.ToolCall{{Name: "read", ID: "r1", Args: map[string]any{"path": "src/a.go"}}},
		},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{
			Outcome: api.ToolResultOutcomeCompleted,
			Content: readJSON,
		}},
	}
	ev := guidance.CloseoutEvidence{Ledger: ledgertest.BuildFromMessages(root, msgs)}
	report := guidance.CoordinatorCompletionReport{
		Synthesis: "Summary.",
		CitedEvidence: []guidance.CoordinatorCitedEvidence{{
			Path: "src/a.go", Line: 1, Excerpt: "package a",
		}},
	}
	out := guidance.BuildCloseoutCitationGrounding(evidence.CitationRoots{ProjectDir: root}, spawn.SurfaceImplementSynthesis, report, ev, guidance.CloseoutGroundingEval{})
	if out == nil || len(out.CitedEvidence) != 1 {
		t.Fatalf("cited_evidence = %+v want 1 entry", out)
	}
	got := out.CitedEvidence[0].Openable
	if got == nil {
		t.Fatal("openable = nil want true")
	}
	if !*got {
		t.Fatal("openable = false want true (regular file on disk)")
	}
}

func TestBuildCloseoutCitationGrounding_emptyLedgerStillReturnsVacuousCheck(t *testing.T) {
	t.Parallel()

	report := guidance.CoordinatorCompletionReport{Synthesis: "Summary only"}
	ev := guidance.CloseoutEvidence{Ledger: evidence.Ledger{}}
	eval := guidance.EvaluateCloseoutCitations(evidence.CitationRoots{}, spawn.SurfaceImplementSynthesis, report, ev)
	out := guidance.BuildCloseoutCitationGrounding(evidence.CitationRoots{}, spawn.SurfaceImplementSynthesis, report, ev, eval)
	if out == nil || !out.Traced {
		t.Fatalf("grounding = %+v want traced vacuous pass", out)
	}
}

func TestBuildCloseoutCitationGroundingRetainsTheDeclaredVerification(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	readJSON := `{"path":"src/a.go","content":"1|package a\n","offset":1,"end_line":1}`
	msgs := []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{Name: "read", ID: "r1", Args: map[string]any{"path": "src/a.go"}}}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted, Content: readJSON}},
	}
	ev := guidance.CloseoutEvidence{Ledger: ledgertest.BuildFromMessages(root, msgs)}
	report := guidance.CoordinatorCompletionReport{
		Synthesis:     "Summary.",
		CitedEvidence: []guidance.CoordinatorCitedEvidence{{Path: "src/a.go", Line: 1, Excerpt: "package a"}},
		Verification:  &verification.Assessment{Method: verification.Project, Reason: "The full suite passed."},
	}
	out := guidance.BuildCloseoutCitationGrounding(evidence.CitationRoots{ProjectDir: root}, spawn.SurfaceImplementSynthesis, report, ev, guidance.CloseoutGroundingEval{})
	if out == nil || out.Verification == nil || out.Verification.Method != "project" || out.Verification.Reason != "The full suite passed." {
		t.Fatalf("declared verification was not retained: %+v", out)
	}
	report.Verification = &verification.Assessment{Method: "guessed", Reason: "x"}
	out = guidance.BuildCloseoutCitationGrounding(evidence.CitationRoots{ProjectDir: root}, spawn.SurfaceImplementSynthesis, report, ev, guidance.CloseoutGroundingEval{})
	if out == nil || out.Verification != nil {
		t.Fatalf("an invalid assessment must not be retained: %+v", out)
	}
}
