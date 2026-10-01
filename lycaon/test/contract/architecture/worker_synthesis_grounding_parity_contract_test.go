package contract

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
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

type parityCitationCase struct {
	name        string
	finding     guidance.WorkerFindingInput
	wantBlocked bool
	synthOnly   bool
	setup       func(t *testing.T) evidence.Ledger
}

func TestWorkerSynthesisGroundingParity_sameInputSameVerdict(t *testing.T) {
	t.Parallel()
	readMsgs := func(root string) []api.Message {
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
	cases := []parityCitationCase{
		{
			name:        "grounded",
			finding:     guidance.WorkerFindingInput{Path: "src/a.go", Line: 1, Excerpt: "package a"},
			wantBlocked: false,
			setup: func(t *testing.T) evidence.Ledger {
				root := t.TempDir()
				return ledgertest.BuildFromMessages(root, readMsgs(root))
			},
		},
		{
			name:        "bare-path",
			finding:     guidance.WorkerFindingInput{Path: "src/a.go"},
			wantBlocked: true,
			setup: func(t *testing.T) evidence.Ledger {
				root := t.TempDir()
				return ledgertest.BuildFromMessages(root, readMsgs(root))
			},
		},
		{
			name:        "wrong-excerpt",
			finding:     guidance.WorkerFindingInput{Path: "src/a.go", Evidence: "read#1", Line: 1, Excerpt: "package b"},
			wantBlocked: false,
			setup: func(t *testing.T) evidence.Ledger {
				root := t.TempDir()
				return ledgertest.BuildFromMessages(root, readMsgs(root))
			},
		},
		{
			name:        "handle-only",
			finding:     guidance.WorkerFindingInput{Evidence: "read#1", Line: 1, Excerpt: "package a"},
			wantBlocked: false,
			synthOnly:   true,
			setup: func(t *testing.T) evidence.Ledger {
				root := t.TempDir()
				return ledgertest.BuildFromMessages(root, readMsgs(root))
			},
		},
		{
			name:        "fabricated",
			finding:     guidance.WorkerFindingInput{Path: "internal/phantom.go", Line: 1, Excerpt: "package phantom"},
			wantBlocked: true,
			setup: func(t *testing.T) evidence.Ledger {
				root := t.TempDir()
				return ledgertest.BuildFromMessages(root, readMsgs(root))
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			ev := tc.setup(t)
			synthEv := guidance.CloseoutEvidence{Ledger: evidence.NamespaceLedger(ev, "leg-a")}

			workerEval := guidance.EvaluateWorkerCitations(evidence.CitationRoots{ProjectDir: root}, []guidance.WorkerFindingInput{tc.finding}, nil, guidance.WorkerNarrativeInput{}, ev)
			synthReport := guidance.CoordinatorCompletionReport{Synthesis: "report"}
			if !tc.synthOnly {
				synthReport.CitedEvidence = []guidance.CoordinatorCitedEvidence{{
					Path: tc.finding.Path, Line: tc.finding.Line, Excerpt: tc.finding.Excerpt,
				}}
			}
			synthEval := guidance.EvaluateCloseoutCitations(evidence.CitationRoots{ProjectDir: root}, "implement_synthesis", synthReport, synthEv)

			workerBlocked := workerEval.Code != ""
			synthBlocked := synthEval.Code != ""
			if !tc.synthOnly && workerBlocked != synthBlocked {
				t.Fatalf("blocked mismatch worker=%v (%q) synthesis=%v (%q)", workerBlocked, workerEval.Code, synthBlocked, synthEval.Code)
			}
			if workerBlocked != tc.wantBlocked {
				t.Fatalf("worker blocked = %v want %v (code=%q)", workerBlocked, tc.wantBlocked, workerEval.Code)
			}
			if !tc.synthOnly && synthBlocked != tc.wantBlocked {
				t.Fatalf("synthesis blocked = %v want %v (code=%q)", synthBlocked, tc.wantBlocked, synthEval.Code)
			}
			if !tc.synthOnly && workerBlocked {
				if worker, ok := synthesisToWorkerGroundingCodePairing[synthEval.Code]; !ok || worker != workerEval.Code {
					t.Fatalf("code pairing worker=%q synth=%q", workerEval.Code, synthEval.Code)
				}
			}
		})
	}
}

func TestWorkerSynthesisGroundingParity_vacuousTypedCitations(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	ev := ledgertest.BuildFromMessages(root, nil)
	if !guidance.TypedCitationChannelsEmpty(0, 0) {
		t.Fatal("empty worker channels should be vacuous")
	}
	emptySynth := guidance.CoordinatorCompletionReport{Synthesis: "done"}
	if !guidance.TypedCitationChannelsEmpty(len(emptySynth.CitedEvidence), len(emptySynth.CitedURLs)) {
		t.Fatal("empty synthesis channels should be vacuous")
	}
	synthEv := guidance.CloseoutEvidence{Ledger: ev}
	synthEval := guidance.EvaluateCloseoutCitations(evidence.CitationRoots{ProjectDir: root}, "implement_synthesis", emptySynth, synthEv)
	if synthEval.Code != "" {
		t.Fatalf("empty synthesis citations should pass, got %q", synthEval.Code)
	}
	synthGrounding := guidance.BuildCloseoutCitationGrounding(evidence.CitationRoots{ProjectDir: root}, "implement_synthesis", emptySynth, synthEv, synthEval)
	if !synthVacuousFromChecks(synthGrounding) {
		t.Fatal("expected vacuous typed_citations check on synthesis audit")
	}

	populated := guidance.CoordinatorCompletionReport{
		Synthesis: "Finding",
		CitedEvidence: []guidance.CoordinatorCitedEvidence{{
			Path: "src/a.go", Line: 1, Excerpt: "package a",
		}},
	}
	msgs := []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "read", ID: "c1", Args: map[string]any{"path": "src/a.go"}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{
			Outcome: api.ToolResultOutcomeCompleted,
			Content: `{"path":"src/a.go","content":"1|package a","offset":1,"end_line":1}`,
		}},
	}
	populatedEv := guidance.CloseoutEvidence{Ledger: evidence.NamespaceLedger(ledgertest.BuildFromMessages(root, msgs), "leg-a")}
	popEval := guidance.EvaluateCloseoutCitations(evidence.CitationRoots{ProjectDir: root}, "implement_synthesis", populated, populatedEv)
	popGrounding := guidance.BuildCloseoutCitationGrounding(evidence.CitationRoots{ProjectDir: root}, "implement_synthesis", populated, populatedEv, popEval)
	if synthVacuousFromChecks(popGrounding) {
		t.Fatal("grounded citation must be non-vacuous")
	}
}

func TestWorkerSynthesisGroundingParity_crossLanguageGoldens(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	fixtureDir := filepath.Join(root, "lycaon", "test", "fixtures", "citation-grounding-parity")
	cases := map[string]citationGroundingParityGolden{
		"none.json":                buildParityGoldenNone(),
		"grounded.json":            buildParityGoldenGrounded(t),
		"prose-advisory-only.json": buildParityGoldenProseAdvisory(t),
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

func TestWorkerSynthesisGroundingParity_evaluateSynthesisUsesSharedResolver(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	path := filepath.Join(root, "lycaon", "internal", "guidance", "closeout_grounding.go")
	body, err := os.ReadFile(path)
	testutil.FailErr(t, "read closeout_grounding.go", err)
	src := string(body)
	for _, fn := range []string{"resolveCloseoutFindingCitations", "citedURLHandleOffenders", "EvaluateWorkerProseLeaks"} {
		if !strings.Contains(src, fn+"(") {
			t.Fatalf("EvaluateCloseoutCitations must call shared %s", fn)
		}
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, body, 0)
	testutil.FailErr(t, "parse closeout_grounding.go", err)
	if hasFuncDecl(f, "citedEvidenceUnverifiableOffenders") || hasFuncDecl(f, "synthesisCitedURLOffenders") {
		t.Fatal("closeout_grounding.go must not re-fork worker resolver helpers")
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

func buildParityGoldenProseAdvisory(t *testing.T) citationGroundingParityGolden {
	t.Helper()
	root := t.TempDir()
	msgs := []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "read", ID: "c1", Args: map[string]any{"path": "src/new.go", "offset": 10, "limit": 1}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{
			Outcome: api.ToolResultOutcomeCompleted,
			Content: `{"path":"src/new.go","content":"10|  package x","offset":10,"end_line":10,"limit":1}`,
		}},
	}
	ev := guidance.CloseoutEvidence{Ledger: evidence.NamespaceLedger(ledgertest.BuildFromMessages(root, msgs), "leg-a")}
	report := guidance.CoordinatorCompletionReport{
		Synthesis: "Issue at src/new.go:10 without typed citation",
	}
	eval := guidance.EvaluateCloseoutCitations(evidence.CitationRoots{ProjectDir: root}, "implement_synthesis", report, ev)
	grounding := guidance.BuildCloseoutCitationGrounding(evidence.CitationRoots{ProjectDir: root}, "implement_synthesis", report, ev, eval)
	return citationGroundingParityGolden{
		HadTraceableWork: true,
		Grounding:        derefGrounding(grounding),
	}
}

func synthVacuousFromChecks(g *api.CitationGrounding) bool {
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

func hasFuncDecl(f *ast.File, name string) bool {
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name == nil || fn.Name.Name != name {
			continue
		}
		return true
	}
	return false
}
