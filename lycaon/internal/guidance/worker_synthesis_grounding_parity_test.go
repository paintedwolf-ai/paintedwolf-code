package guidance_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/guidance/ledgertest"
	"github.com/lycaon/lycaon/pkg/api"
)

// parityReadMessages is one completed read of src/a.go line 1.
func parityReadMessages() []api.Message {
	return []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "read", ID: "c1", Args: map[string]any{"path": "src/a.go"}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{
			Outcome: api.ToolResultOutcomeCompleted,
			Content: `{"path":"src/a.go","content":"1|package a","offset":1,"end_line":1}`,
		}},
	}
}

// The worker and synthesis evaluators give the same citation the same
// verdict, each under its own surface's code.
func TestWorkerSynthesisGroundingParity_sameInputSameVerdict(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		finding    guidance.WorkerFindingInput
		synthOnly  bool
		wantWorker string
		wantSynth  string
	}{
		{name: "grounded", finding: guidance.WorkerFindingInput{Path: "src/a.go", Line: 1, Excerpt: "package a"}},
		{name: "bare-path", finding: guidance.WorkerFindingInput{Path: "src/a.go"},
			wantWorker: guidance.WorkerEvidenceHandleUnknownCode, wantSynth: guidance.SynthCitationUnverifiableCode},
		{name: "wrong-excerpt", finding: guidance.WorkerFindingInput{Path: "src/a.go", Evidence: "read#1", Line: 1, Excerpt: "package b"}},
		{name: "handle-only", finding: guidance.WorkerFindingInput{Evidence: "read#1", Line: 1, Excerpt: "package a"}, synthOnly: true},
		{name: "fabricated", finding: guidance.WorkerFindingInput{Path: "internal/phantom.go", Line: 1, Excerpt: "package phantom"},
			wantWorker: guidance.WorkerEvidenceHandleUnknownCode, wantSynth: guidance.SynthHandleNotInLegsCode},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			ev := ledgertest.BuildFromMessages(root, parityReadMessages())
			workerEval := guidance.EvaluateWorkerCitations(evidence.CitationRoots{ProjectDir: root}, []guidance.WorkerFindingInput{tc.finding}, nil, guidance.WorkerNarrativeInput{}, ev)
			if workerEval.Code != tc.wantWorker {
				t.Fatalf("worker code = %q, want %q", workerEval.Code, tc.wantWorker)
			}
			report := guidance.CoordinatorCompletionReport{Synthesis: "report"}
			if !tc.synthOnly {
				report.CitedEvidence = []guidance.CoordinatorCitedEvidence{{Path: tc.finding.Path, Line: tc.finding.Line, Excerpt: tc.finding.Excerpt}}
			}
			synthEv := guidance.CloseoutEvidence{Ledger: evidence.NamespaceLedger(ev, "leg-a")}
			synthEval := guidance.EvaluateCloseoutCitations(evidence.CitationRoots{ProjectDir: root}, "implement_synthesis", report, synthEv)
			if synthEval.Code != tc.wantSynth {
				t.Fatalf("synthesis code = %q, want %q", synthEval.Code, tc.wantSynth)
			}
		})
	}
}

// Empty typed citation channels audit as vacuous; a grounded citation does not.
func TestWorkerSynthesisGroundingParity_vacuousTypedCitations(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if !guidance.TypedCitationChannelsEmpty(0, 0) {
		t.Fatal("empty worker channels should be vacuous")
	}
	empty := guidance.CoordinatorCompletionReport{Synthesis: "done"}
	if !guidance.TypedCitationChannelsEmpty(len(empty.CitedEvidence), len(empty.CitedURLs)) {
		t.Fatal("empty synthesis channels should be vacuous")
	}
	emptyEv := guidance.CloseoutEvidence{Ledger: ledgertest.BuildFromMessages(root, nil)}
	emptyEval := guidance.EvaluateCloseoutCitations(evidence.CitationRoots{ProjectDir: root}, "implement_synthesis", empty, emptyEv)
	if emptyEval.Code != "" {
		t.Fatalf("empty synthesis citations should pass, got %q", emptyEval.Code)
	}
	if g := guidance.BuildCloseoutCitationGrounding(evidence.CitationRoots{ProjectDir: root}, "implement_synthesis", empty, emptyEv, emptyEval); !citationAuditVacuous(g) {
		t.Fatalf("empty synthesis audit = %+v, want vacuous", g)
	}

	populated := guidance.CoordinatorCompletionReport{
		Synthesis:     "Finding",
		CitedEvidence: []guidance.CoordinatorCitedEvidence{{Path: "src/a.go", Line: 1, Excerpt: "package a"}},
	}
	populatedEv := guidance.CloseoutEvidence{Ledger: evidence.NamespaceLedger(ledgertest.BuildFromMessages(root, parityReadMessages()), "leg-a")}
	populatedEval := guidance.EvaluateCloseoutCitations(evidence.CitationRoots{ProjectDir: root}, "implement_synthesis", populated, populatedEv)
	if populatedEval.Code != "" {
		t.Fatalf("grounded synthesis citation should pass, got %q", populatedEval.Code)
	}
	if g := guidance.BuildCloseoutCitationGrounding(evidence.CitationRoots{ProjectDir: root}, "implement_synthesis", populated, populatedEv, populatedEval); citationAuditVacuous(g) {
		t.Fatalf("grounded synthesis audit = %+v, want non-vacuous", g)
	}
}

func citationAuditVacuous(g *api.CitationGrounding) bool {
	if g == nil {
		return true
	}
	for _, check := range g.Checks {
		if check.Kind == api.CitationGroundingCheckKindCitation && !check.Vacuous {
			return false
		}
	}
	return len(g.CitedEvidence) == 0 && len(g.Findings) == 0 && len(g.CitedURLs) == 0
}
