package guidance_test

import (
	"context"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/guidance/ledgertest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestUserIntentBoundary_ignoresInternalUserRows(t *testing.T) {
	t.Parallel()
	visible := api.Message{Role: api.MessageRoleUser, Content: "ship the feature"}
	internalKick := api.Message{
		Role:       api.MessageRoleUser,
		Content:    "Worker task finished — read envelope report_json first",
		Visibility: api.MessageVisibilityInternal,
	}
	internalWake := api.Message{
		Role:       api.MessageRoleUser,
		Content:    "[host:loop-wake]",
		Visibility: api.MessageVisibilityInternal,
	}
	internalReject := api.Message{
		Role:       api.MessageRoleUser,
		Content:    "Rejected: Synthesis cited 2 path(s) not present in worker leg evidence.",
		Visibility: api.MessageVisibilityInternal,
	}
	envelope := api.Message{
		Role:    api.MessageRoleTool,
		Content: `<task child_session_id="child-1">leg</task>`,
	}

	cases := []struct {
		name    string
		history []api.Message
		want    int
	}{
		{
			name:    "visible user only",
			history: []api.Message{visible},
			want:    1,
		},
		{
			name: "host kick after visible user",
			history: []api.Message{
				visible, envelope, internalKick,
			},
			want: 1,
		},
		{
			name: "loop wake after kick",
			history: []api.Message{
				visible, envelope, internalKick, internalWake,
			},
			want: 1,
		},
		{
			name: "multiple guard reject nudges",
			history: []api.Message{
				visible, envelope, internalKick, internalWake, internalReject, internalReject,
			},
			want: 1,
		},
		{
			name: "second visible user starts new turn",
			history: []api.Message{
				visible, envelope, internalReject,
				{Role: api.MessageRoleUser, Content: "also add tests"},
			},
			want: 4,
		},
		{
			name:    "host-only transcript uses full history",
			history: []api.Message{internalKick, internalWake, envelope},
			want:    0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := api.UserIntentBoundary(tc.history); got != tc.want {
				t.Fatalf("UserIntentBoundary = %d want %d", got, tc.want)
			}
		})
	}
}

func TestUnionCloseoutEvidence_legsComeFromDispatchRecordsNotHistory(t *testing.T) {
	root := t.TempDir()
	readJSON := `{"content":"1|#!/usr/bin/env bash","path":"run.sh","offset":1,"end_line":1}`
	child := []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{Name: "read", ID: "c1", Args: map[string]any{"path": "run.sh"}}}},
		{Role: api.MessageRoleTool, Content: readJSON, ToolResult: &api.ToolResult{Content: readJSON, Outcome: api.ToolResultOutcomeCompleted}},
	}
	lookup := func(id string) []api.Message {
		if id == "child-pe" {
			return child
		}
		return nil
	}
	// A compacted transcript keeps the request but no longer holds the leg's summary.
	compacted := []api.Message{
		{Role: api.MessageRoleUser, Content: "make a venv and start script", CreatedAt: time.Unix(100, 0).UTC()},
		{Role: api.MessageRoleUser, Content: "[host:loop-wake]", Origin: api.MessageOriginHost, Visibility: api.MessageVisibilityInternal},
	}
	reader := ledgertest.CloseoutReader(root, []guidance.EvidenceLeg{{ChildSessionID: "child-pe"}}, lookup)
	ev, err := guidance.UnionCloseoutEvidence(context.Background(), reader, "parent-1", compacted)
	testutil.FailErr(t, "union closeout evidence", err)
	if !evidence.PathObserved(ev.Ledger, "run.sh") {
		t.Fatalf("compacted history dropped the leg's evidence; paths=%v", evidence.ObservedPathsSorted(ev.Ledger))
	}
	if _, ok := evidence.ResolveHandle(ev.Ledger, "child-pe:read#1"); !ok {
		t.Fatalf("leg without an id should namespace by child session: %v", evidence.HandlesSorted(ev.Ledger))
	}
}

func TestUnionCloseoutEvidence_asksForLegsSinceTheUserIntent(t *testing.T) {
	intent := time.Unix(200, 0).UTC()
	history := []api.Message{
		{Role: api.MessageRoleUser, Content: "earlier", CreatedAt: time.Unix(100, 0).UTC()},
		{Role: api.MessageRoleUser, Content: "current request", CreatedAt: intent},
		{Role: api.MessageRoleUser, Content: "Worker task finished", Visibility: api.MessageVisibilityInternal, CreatedAt: time.Unix(300, 0).UTC()},
	}
	reader := &sinceRecordingReader{}
	_, err := guidance.UnionCloseoutEvidence(context.Background(), reader, "parent-1", history)
	testutil.FailErr(t, "union closeout evidence", err)
	if !reader.since.Equal(intent) {
		t.Fatalf("since = %v want the current user intent %v", reader.since, intent)
	}
	_, err = guidance.UnionCloseoutEvidence(context.Background(), reader, "parent-1", history[2:])
	testutil.FailErr(t, "union closeout evidence without intent", err)
	if !reader.since.IsZero() {
		t.Fatalf("since = %v want every leg when the history holds no user intent", reader.since)
	}
}

type sinceRecordingReader struct{ since time.Time }

func (r *sinceRecordingReader) LoadLedger(context.Context, string) (evidence.Ledger, error) {
	return evidence.Ledger{}, nil
}

func (r *sinceRecordingReader) WorkerLegs(_ context.Context, _ string, since time.Time) ([]guidance.EvidenceLeg, error) {
	r.since = since
	return nil, nil
}

func TestUnionLegEvidence_childLedgerObserved(t *testing.T) {
	root := t.TempDir()
	grepJSON := `{"matches":[{"path":"src/a.go","line":1,"content":"package a"}]}`
	child := []api.Message{
		{
			Role: api.MessageRoleAssistant,
			ToolCalls: []api.ToolCall{
				{Name: "grep", ID: "c1", Args: map[string]any{"path": ".", "pattern": "package"}},
			},
		},
		{Role: api.MessageRoleTool, Content: grepJSON, ToolResult: &api.ToolResult{Content: grepJSON, Outcome: api.ToolResultOutcomeCompleted}},
	}
	ev := unionLegEvidence(root, []guidance.EvidenceLeg{{ChildSessionID: "child-1"}}, func(id string) []api.Message {
		if id == "child-1" {
			return child
		}
		return nil
	})
	if !evidence.PathObserved(ev.Ledger, "src/a.go") {
		t.Fatalf("missing src/a.go; paths=%v", evidence.ObservedPathsSorted(ev.Ledger))
	}
}

func TestUnionLegEvidence_legIDNamespacesHandles(t *testing.T) {
	root := t.TempDir()
	childRead := `{"path":"internal/worker.go","content":"1|package worker","offset":1,"end_line":1}`
	child := []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{Name: "read", ID: "r1", Args: map[string]any{"path": "internal/worker.go"}}}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted, Content: childRead}},
	}
	ev := unionLegEvidence(root, []guidance.EvidenceLeg{{ChildSessionID: "child-1", LegID: "leg-1"}}, func(id string) []api.Message {
		if id == "child-1" {
			return child
		}
		return nil
	})
	if !evidence.PathObserved(ev.Ledger, "internal/worker.go") {
		t.Fatalf("leg ledger omitted worker path: %v", evidence.ObservedPathsSorted(ev.Ledger))
	}
	if _, ok := evidence.ResolveHandle(ev.Ledger, "leg-1:read#1"); !ok {
		t.Fatalf("leg id did not namespace child handles: %v", evidence.HandlesSorted(ev.Ledger))
	}
}

func TestUnionCloseoutEvidence_envelopeInHistoryAddsNoLeg(t *testing.T) {
	root := t.TempDir()
	childRead := `{"path":"internal/worker.go","content":"1|package worker","offset":1,"end_line":1}`
	child := []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{Name: "read", ID: "r1", Args: map[string]any{"path": "internal/worker.go"}}}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted, Content: childRead}},
	}
	history := []api.Message{
		{Role: api.MessageRoleUser, Content: "survey"},
		{
			Role:    api.MessageRoleTool,
			Content: `<task job_id="job-1" child_session_id="child-1" state="complete"><summary>done</summary></task>`,
			WorkerSummary: &api.WorkerSummaryMeta{
				WorkerID: "job-1", ChildSessionID: "child-1", Status: api.WorkerSummaryStatusComplete,
			},
		},
	}
	reader := ledgertest.CloseoutReader(root, nil, func(id string) []api.Message {
		if id == "child-1" {
			return child
		}
		return nil
	})
	ev, err := guidance.UnionCloseoutEvidence(context.Background(), reader, "parent-1", history)
	testutil.FailErr(t, "union closeout evidence", err)
	if evidence.PathObserved(ev.Ledger, "internal/worker.go") {
		t.Fatal("transcript content supplied a leg the dispatch records do not name")
	}
}

func TestUnionCloseoutEvidence_mergesCoordinatorAndWorkerLedgers(t *testing.T) {
	root := t.TempDir()
	parentRead := `{"path":"README.md","content":"1|hello\n","offset":1,"end_line":1}`
	parentMsgs := []api.Message{
		{
			Role:      api.MessageRoleAssistant,
			ToolCalls: []api.ToolCall{{Name: "read", ID: "r1", Args: map[string]any{"path": "README.md"}}},
		},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{
			Outcome: api.ToolResultOutcomeCompleted,
			Content: parentRead,
		}},
	}
	childGrep := `{"matches":[{"path":"internal/a.go","line":1,"content":"package a"}]}`
	childMsgs := []api.Message{
		{
			Role:      api.MessageRoleAssistant,
			ToolCalls: []api.ToolCall{{Name: "grep", ID: "g1", Args: map[string]any{"path": ".", "pattern": "a"}}},
		},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{
			Outcome: api.ToolResultOutcomeCompleted,
			Content: childGrep,
		}},
	}
	history := []api.Message{{Role: api.MessageRoleUser, Content: "go"}}
	reader := ledgertest.CloseoutReader(root, []guidance.EvidenceLeg{{ChildSessionID: "child-1", LegID: "leg-a"}}, func(id string) []api.Message {
		switch id {
		case "parent-1":
			return parentMsgs
		case "child-1":
			return childMsgs
		default:
			return nil
		}
	})
	ev, err := guidance.UnionCloseoutEvidence(context.Background(), reader, "parent-1", history)
	testutil.FailErr(t, "guidance.UnionCloseoutEvidence failed", err)
	if !evidence.PathObserved(ev.Ledger, "README.md") {
		t.Fatal("missing coordinator README.md in closeout union")
	}
	if !evidence.PathObserved(ev.Ledger, "internal/a.go") {
		t.Fatal("missing worker internal/a.go in closeout union")
	}
}

func TestUnionLegEvidence_envelopeURLNotObserved(t *testing.T) {
	root := t.TempDir()
	ev := unionLegEvidence(root, []guidance.EvidenceLeg{{ChildSessionID: "child-1"}}, func(string) []api.Message { return nil })
	if ev.URLSeen("https://fabricated.example/doc") {
		t.Fatal("envelope prose must not ground synthesis URLs")
	}
}

func TestEvaluateCloseoutCitationsUnobservedURLRequiresRepair(t *testing.T) {
	for _, surface := range []string{"implement_investigate", "implement_synthesis"} {
		report := guidance.CoordinatorCompletionReport{Synthesis: "See docs", CitedURLs: []string{"https://example.com/docs"}}
		eval := guidance.EvaluateCloseoutCitations(evidence.CitationRoots{}, surface, report, guidance.CloseoutEvidence{})
		want := guidance.SynthURLNotObservedCode
		if surface == "implement_investigate" {
			want = guidance.InvestURLNotObservedCode
		}
		if eval.Code != want || len(eval.UnobservedURLs) != 1 || eval.UnobservedURLs[0] != report.CitedURLs[0] {
			t.Fatalf("%s URL evaluation = %+v, want %s", surface, eval, want)
		}
	}
}

func TestEvaluateCloseoutCitations_ungroundedHandle(t *testing.T) {
	ev := guidance.CloseoutEvidence{
		Ledger: evidenceWithObservedPaths("src/a.go"),
	}
	report := guidance.CoordinatorCompletionReport{
		Synthesis: "Added handler",
		CitedEvidence: []guidance.CoordinatorCitedEvidence{{
			Path: "src/missing.go", Line: 1, Excerpt: "missing",
		}},
	}
	eval := guidance.EvaluateCloseoutCitations(evidence.CitationRoots{ProjectDir: t.TempDir()}, "implement_synthesis", report, ev)
	if eval.Code != guidance.SynthHandleNotInLegsCode {
		t.Fatalf("code=%q want %q", eval.Code, guidance.SynthHandleNotInLegsCode)
	}
	// Unresolved worker handles remain visible as failed checks.
	g := guidance.BuildCloseoutCitationGrounding(evidence.CitationRoots{ProjectDir: t.TempDir()}, "implement_synthesis", report, ev, eval)
	if g == nil || g.Traced || g.HintCode != guidance.SynthHandleNotInLegsCode {
		t.Fatalf("audit = %+v want untraced failing audit with hint code", g)
	}
	if len(g.Checks) == 0 || g.Checks[0].Status != api.CitationGroundingCheckStatusFailed {
		t.Fatalf("audit checks = %+v want a failing check", g.Checks)
	}
}

func TestEvaluateCloseoutCitations_groundedHandle(t *testing.T) {
	msgs := []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "grep", ID: "c1", Args: map[string]any{"path": ".", "pattern": "a"}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{
			Outcome: api.ToolResultOutcomeCompleted,
			Content: `{"matches":[{"path":"src/a.go","line":1,"content":"package a"}]}`,
		}},
	}
	ev := guidance.CloseoutEvidence{
		Ledger: evidence.NamespaceLedger(ledgertest.BuildFromMessages("", msgs), "leg-a"),
	}
	report := guidance.CoordinatorCompletionReport{
		Synthesis: "Updated handler",
		CitedEvidence: []guidance.CoordinatorCitedEvidence{{
			Path: "src/a.go", Line: 1, Excerpt: "package a",
		}},
	}
	eval := guidance.EvaluateCloseoutCitations(evidence.CitationRoots{ProjectDir: t.TempDir()}, "implement_synthesis", report, ev)
	if eval.Code != "" {
		t.Fatalf("code=%q want grounded (empty)", eval.Code)
	}
	if g := guidance.BuildCloseoutCitationGrounding(evidence.CitationRoots{ProjectDir: t.TempDir()}, "implement_synthesis", report, ev, eval); g == nil || !g.Traced {
		t.Fatalf("audit = %+v want traced pass audit", g)
	}
}

func TestEvaluateCloseoutCitations_proseLeakIsAdvisory(t *testing.T) {
	msgs := []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "read", ID: "c1", Args: map[string]any{"path": "src/new.go", "offset": 10, "limit": 1}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{
			Outcome: api.ToolResultOutcomeCompleted,
			Content: `{"path":"src/new.go","content":"10|  package x","offset":10,"end_line":10,"limit":1}`,
		}},
	}
	ev := guidance.CloseoutEvidence{
		Ledger: evidence.NamespaceLedger(ledgertest.BuildFromMessages("", msgs), "leg-a"),
	}
	report := guidance.CoordinatorCompletionReport{
		Synthesis: "Issue at src/new.go:10 without typed citation",
	}
	eval := guidance.EvaluateCloseoutCitations(evidence.CitationRoots{ProjectDir: t.TempDir()}, "implement_synthesis", report, ev)
	if eval.Code != "" {
		t.Fatalf("code=%q want no code — synthesis prose placement is advisory", eval.Code)
	}
	if eval.ProseAdvisoryCount != 1 {
		t.Fatalf("ProseAdvisoryCount = %d want 1 advisory surfaced", eval.ProseAdvisoryCount)
	}
	grounding := guidance.BuildCloseoutCitationGrounding(evidence.CitationRoots{ProjectDir: t.TempDir()}, "implement_synthesis", report, ev, eval)
	statuses := map[string]api.CitationGroundingCheckStatus{}
	for _, check := range grounding.Checks {
		statuses[check.ID] = check.Status
	}
	if statuses["typed_citations"] != api.CitationGroundingCheckStatusPassed ||
		statuses["prose_advisories"] != api.CitationGroundingCheckStatusAdvisory {
		t.Fatalf("check statuses = %v want vacuous typed pass plus prose advisory", statuses)
	}
}

func TestEvaluateCloseoutCitations_barePathCitedEvidenceFlags(t *testing.T) {
	ev := guidance.CloseoutEvidence{
		Ledger: evidenceWithObservedPaths("src/a.go"),
	}
	report := guidance.CoordinatorCompletionReport{
		Synthesis:     "Transport layer lives under src/transport/",
		CitedEvidence: []guidance.CoordinatorCitedEvidence{{Path: "lycaon-den/src/transport/"}},
	}
	eval := guidance.EvaluateCloseoutCitations(evidence.CitationRoots{ProjectDir: t.TempDir()}, "implement_synthesis", report, ev)
	if eval.Code != guidance.SynthHandleNotInLegsCode {
		t.Fatalf("code=%q want bare path in cited_evidence flagged", eval.Code)
	}
}

func TestEvaluateCloseoutCitations_fabricatedTransportPathFlags(t *testing.T) {
	msgs := []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "grep", ID: "c1", Args: map[string]any{"path": ".", "pattern": "a"}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{
			Outcome: api.ToolResultOutcomeCompleted,
			Content: `{"matches":[{"path":"src/a.go","line":1,"content":"package a"}]}`,
		}},
	}
	ev := guidance.CloseoutEvidence{
		Ledger: evidence.NamespaceLedger(ledgertest.BuildFromMessages("", msgs), "leg-a"),
	}
	report := guidance.CoordinatorCompletionReport{
		Synthesis: "Den transport layer",
		CitedEvidence: []guidance.CoordinatorCitedEvidence{{
			Path: "lycaon-den/src/transport/", Line: 1, Excerpt: "package transport",
		}},
	}
	eval := guidance.EvaluateCloseoutCitations(evidence.CitationRoots{ProjectDir: t.TempDir()}, "implement_synthesis", report, ev)
	if eval.Code != guidance.SynthHandleNotInLegsCode {
		t.Fatalf("code=%q want fabricated transport path flagged", eval.Code)
	}
}

func TestBuildCloseoutCitationGrounding_populatedCitedEvidenceNonVacuous(t *testing.T) {
	root := t.TempDir()
	readJSON := `{"content":"1|package a","path":"src/a.go","offset":1,"end_line":1}`
	child := []api.Message{
		{
			Role:      api.MessageRoleAssistant,
			ToolCalls: []api.ToolCall{{Name: "read", ID: "c1", Args: map[string]any{"path": "src/a.go"}}},
		},
		{Role: api.MessageRoleTool, Content: readJSON, ToolResult: &api.ToolResult{Content: readJSON, Outcome: api.ToolResultOutcomeCompleted}},
	}
	ev := unionLegEvidence(root, []guidance.EvidenceLeg{{ChildSessionID: "child-1"}}, func(id string) []api.Message {
		if id == "child-1" {
			return child
		}
		return nil
	})
	report := guidance.CoordinatorCompletionReport{
		Synthesis:     "Updated handler.",
		CitedEvidence: []guidance.CoordinatorCitedEvidence{{Path: "src/a.go", Line: 1, Excerpt: "package a"}},
	}
	out := guidance.BuildCloseoutCitationGrounding(evidence.CitationRoots{ProjectDir: root}, "implement_synthesis", report, ev, guidance.CloseoutGroundingEval{})
	if out == nil {
		t.Fatal("expected grounding payload")
	}
	var vacuous bool
	for _, check := range out.Checks {
		if check.ID == "typed_citations" && check.Vacuous {
			vacuous = true
		}
	}
	if vacuous {
		t.Fatalf("checks = %+v want non-vacuous when cited_evidence populated", out.Checks)
	}
	if len(out.CitedEvidence) == 0 {
		t.Fatal("expected cited_evidence on wire")
	}
}
