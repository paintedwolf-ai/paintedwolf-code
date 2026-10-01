package guidance_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/guidance/ledgertest"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestEvaluateCloseoutCitationsTracesSecondaryRoot(t *testing.T) {
	t.Parallel()

	primary := t.TempDir()
	secondary := t.TempDir()
	secondaryPath := filepath.Join(secondary, "lib", "secondary.go")
	testutil.FailErr(t, "create secondary directory", os.MkdirAll(filepath.Dir(secondaryPath), 0o755))
	testutil.FailErr(t, "write secondary file", os.WriteFile(secondaryPath, []byte("package lib\n"), 0o644))
	roots := evidence.CitationRoots{
		Roots: []projectroot.RootRef{
			{ID: "primary", Label: "app", Path: primary, IsPrimary: true},
			{ID: "secondary", Label: "shared", Path: secondary},
		},
		ActiveRootID: "primary",
		ProjectDir:   primary,
	}
	path := "@shared/lib/secondary.go"
	ev := guidance.CloseoutEvidence{Ledger: evidence.AssembleLedger([]evidence.Record{{
		Handle:     "read#1",
		Kind:       "read",
		Shape:      evidence.ShapeFileRegion,
		Path:       path,
		Body:       []string{"1|package lib"},
		LineRanges: []evidence.LineRange{{Start: 1, End: 1}},
	}})}
	report := guidance.CoordinatorCompletionReport{
		Synthesis: "Secondary package inspected.",
		CitedEvidence: []guidance.CoordinatorCitedEvidence{{
			Path: path, Line: 1, Excerpt: "package lib",
		}},
	}

	eval := guidance.EvaluateCloseoutCitations(roots, "implement_investigate", report, ev)
	if eval.Code != "" || len(eval.Offenders) != 0 {
		t.Fatalf("secondary-root evaluation = %+v want grounded", eval)
	}
	grounding := guidance.BuildCloseoutCitationGrounding(roots, "implement_investigate", report, ev, eval)
	if grounding == nil || len(grounding.CitedEvidence) != 1 {
		t.Fatalf("grounding = %+v want one secondary-root citation", grounding)
	}
	if openable := grounding.CitedEvidence[0].Openable; openable == nil || !*openable {
		t.Fatalf("secondary-root citation openable = %v want true", openable)
	}
}

func TestEvaluateCloseoutCitations_commandReadPathTracesParaphrase(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	body := `{"stages":[{"command":"file docs/coordination.md","exit_code":0}],"exit_code":0,"tail":"docs/coordination.md: Unicode text\n# Coordinator execution modes\n\n**SSOT** for orchestrate.\n","ok":true}`
	msgs := []api.Message{
		{
			Role: api.MessageRoleAssistant,
			ToolCalls: []api.ToolCall{
				{Name: "command", ID: "c1", Args: map[string]any{"command": "file docs/coordination.md"}},
			},
		},
		{
			Role:       api.MessageRoleTool,
			Content:    body,
			ToolResult: &api.ToolResult{Content: body, Outcome: api.ToolResultOutcomeCompleted},
		},
	}
	ev := ledgertest.BuildFromMessages(root, msgs)
	report := guidance.CoordinatorCompletionReport{
		Synthesis: "Summary.",
		CitedEvidence: []guidance.CoordinatorCitedEvidence{{
			Path:    "docs/coordination.md",
			Line:    1,
			Excerpt: "SSOT for orchestrate vs investigate coordinator behavior.",
		}},
	}
	eval := guidance.EvaluateCloseoutCitations(evidence.CitationRoots{ProjectDir: root}, "implement_investigate", report, guidance.CloseoutEvidence{Ledger: ev})
	if eval.Code != "" {
		t.Fatalf("command-read path with paraphrased excerpt should trace, not fail: code=%q offenders=%v", eval.Code, eval.Offenders)
	}
	res := evidence.Resolve(evidence.CitationRoots{ProjectDir: root}, evidence.Triple{
		Path:    "docs/coordination.md",
		Line:    1,
		Excerpt: report.CitedEvidence[0].Excerpt,
	}, ev, "")
	if res.Verdict == evidence.VerdictUnverifiable {
		t.Fatalf("resolution = %+v want traced/matched, not unverifiable", res)
	}
}

func TestEvaluateCloseoutCitations_unreadPathFails(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	ev := evidence.Ledger{Handles: map[string]evidence.Record{}, ByPath: map[string][]string{}, PathFidelity: map[string]string{}}
	report := guidance.CoordinatorCompletionReport{
		Synthesis: "Summary.",
		CitedEvidence: []guidance.CoordinatorCitedEvidence{{
			Path:    "docs/never-read.md",
			Line:    1,
			Excerpt: "claim",
		}},
	}
	eval := guidance.EvaluateCloseoutCitations(evidence.CitationRoots{ProjectDir: root}, "implement_investigate", report, guidance.CloseoutEvidence{Ledger: ev})
	if eval.Code != guidance.InvestHandleNotObservedCode {
		t.Fatalf("code = %q want %s", eval.Code, guidance.InvestHandleNotObservedCode)
	}
}
