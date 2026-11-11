package closeoutassembly

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/citationmd"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/guidance/ledgertest"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/verification"
	"github.com/lycaon/lycaon/pkg/api"
)

func draftOf(markdown string) guidance.CloseoutDraft {
	return guidance.ParseCloseoutDraft(markdown, citationmd.Options{})
}

func TestAssembleFanoutCloseoutFromLegs(t *testing.T) {
	legs := []legCloseout{
		{
			key: "job-a", legID: "leg-a",
			title: "auth",
			brief: "Rate limiter added to login.",
			cited: []api.CitationGroundingCitedEvidence{{Handle: "read#1", Path: "auth/login.go", Line: 12}},
		},
		{
			key: "job-b", legID: "leg-b",
			title: "billing",
			brief: "Invoice totals now reconcile.",
			cited: []api.CitationGroundingCitedEvidence{{Handle: "read#1", Path: "billing/invoice.go", Line: 40}},
		},
	}
	ev := guidance.CloseoutEvidence{Ledger: evidence.AssembleLedger([]evidence.Record{
		{Handle: "leg-a:read#1", Kind: "read", Shape: evidence.ShapeFileRegion, Path: "auth/login.go", LineRanges: []evidence.LineRange{{Start: 12, End: 12}}},
		{Handle: "leg-b:read#1", Kind: "read", Shape: evidence.ShapeFileRegion, Path: "billing/invoice.go", LineRanges: []evidence.LineRange{{Start: 40, End: 40}}},
	})}

	report, grounding := assembleGroundedLedgerCloseout(evidence.CitationRoots{}, "implement_synthesis", ev, legs, guidance.CloseoutDraft{}, nil, []string{"SYNTH_HANDLE_NOT_IN_LEGS"}, 3)

	if !strings.Contains(report.Synthesis, "Rate limiter added to login.") ||
		!strings.Contains(report.Synthesis, "Invoice totals now reconcile.") {
		t.Fatalf("fan-out body missing leg briefs: %q", report.Synthesis)
	}
	if grounding == nil {
		t.Fatal("expected grounding envelope")
	}
	if !grounding.HostAssembled {
		t.Fatal("fan-out grounding must be host_assembled")
	}
	if !grounding.Traced {
		t.Fatal("fan-out grounding is built from grounded leg records → traced")
	}
	if len(grounding.CitedEvidence) != 2 {
		t.Fatalf("cited_evidence = %d, want union of 2 leg handles", len(grounding.CitedEvidence))
	}
	if strings.Contains(report.Synthesis, "Reason code:") {
		t.Fatalf("assembly provenance belongs in grounding, not user prose: %q", report.Synthesis)
	}
	if grounding.HintCode != "SYNTH_HANDLE_NOT_IN_LEGS" {
		t.Fatalf("hint_code=%q want SYNTH_HANDLE_NOT_IN_LEGS", grounding.HintCode)
	}
	if grounding.RetryCount != 3 {
		t.Fatalf("retry_count=%d want 3", grounding.RetryCount)
	}
}

func TestCoordinatorCitationFromLegNamespacesWorkerHandle(t *testing.T) {
	got := coordinatorCitationFromLeg("leg-1", api.CitationGroundingCitedEvidence{
		Handle: "read#1",
		Path:   "worker.go",
		Line:   7,
	})
	if got.Evidence != "leg-1:read#1" || got.Path != "worker.go" || got.Line != 7 {
		t.Fatalf("citation = %+v", got)
	}
}

func TestStructuredWorkerSummaryCloseoutPreservesPinnedReport(t *testing.T) {
	const pinned = "## Architecture\n\nThe sidecar handles orchestration and the Den renders state."
	envelope := workercompletion.FormatWorkerCompletionEnvelope(workercompletion.WorkerCompletionEnvelope{
		JobID:          "job-1",
		ChildSessionID: "child-1",
		AgentType:      "repo-researcher",
		State:          "complete",
		Report: workercompletion.WorkerCompletionReport{
			LegStatus: "complete",
			Brief:     "Mapped the orchestration boundary.",
		},
	})
	history := []api.Message{
		{Role: api.MessageRoleUser, Content: "explain the app"},
		{
			Role:    api.MessageRoleTool,
			Content: `{"job_id":"job-1","status":"enqueued"}`,
			WorkerSummary: &api.WorkerSummaryMeta{
				WorkerID:       "job-1",
				LegID:          "leg-1",
				ChildSessionID: "child-1",
				AgentType:      "repo-researcher",
				Status:         api.WorkerSummaryStatusComplete,
				Envelope:       envelope,
				Grounding: &api.CitationGrounding{Traced: true, CitedEvidence: []api.CitationGroundingCitedEvidence{{
					Handle: "read#1", Path: "internal/session.go", Line: 1, Excerpt: "package session",
				}}},
			},
		},
	}
	legs := terminalWorkerSummaryCloseouts(history)
	if len(legs) != 1 || legs[0].brief != "Mapped the orchestration boundary." {
		t.Fatalf("structured legs = %+v", legs)
	}
	ev := guidance.CloseoutEvidence{Ledger: evidence.AssembleLedger([]evidence.Record{{
		Handle: "leg-1:read#1", Kind: "read", Shape: evidence.ShapeFileRegion,
		Path: "internal/session.go", Body: []string{"1|package session"},
		LineRanges: []evidence.LineRange{{Start: 1, End: 1}},
	}})}
	report, grounding := assembleGroundedLedgerCloseout(
		evidence.CitationRoots{}, "implement_synthesis", ev, legs, draftOf(pinned), nil,
		[]string{guidance.SynthHandleNotInLegsCode}, 2,
	)
	if report.Synthesis != pinned {
		t.Fatalf("fallback replaced pinned report: %q", report.Synthesis)
	}
	if grounding == nil || !grounding.Traced || !grounding.HostAssembled || len(grounding.CitedEvidence) != 1 {
		t.Fatalf("worker references must remain inspectable beside the pinned answer: %+v", grounding)
	}
}

func TestLedgerFallbackDoesNotMarkStaleWorkerCitationTraced(t *testing.T) {
	ev := guidance.CloseoutEvidence{Ledger: evidence.AssembleLedger([]evidence.Record{{
		Handle: "leg-1:read#1", Kind: "read", Shape: evidence.ShapeFileRegion,
		Path: "observed.go", Body: []string{"1|package observed"},
		LineRanges: []evidence.LineRange{{Start: 1, End: 1}},
	}})}
	legs := []legCloseout{{
		key: "job-1", legID: "leg-1", brief: "Surveyed the package.",
		cited: []api.CitationGroundingCitedEvidence{{Handle: "read#99", Path: "phantom.go", Line: 1}},
	}}
	report, grounding := assembleGroundedLedgerCloseout(
		evidence.CitationRoots{}, "implement_synthesis", ev, legs, draftOf("Pinned report."), nil,
		[]string{guidance.SynthHandleNotInLegsCode}, 3,
	)
	if report.Synthesis != "Pinned report." {
		t.Fatalf("fallback replaced pinned report: %q", report.Synthesis)
	}
	if grounding == nil || grounding.Traced || !grounding.HostAssembled {
		t.Fatalf("stale worker citation must remain traced=false: %+v", grounding)
	}
	if len(grounding.CitedEvidence) != 1 || grounding.CitedEvidence[0].Handle != "leg-1:read#1" {
		t.Fatalf("fallback must attach the observed reference, never the stale worker handle: %+v", grounding.CitedEvidence)
	}
}

func TestAssembleInvestigateCloseoutBindsSecondaryRootEvidence(t *testing.T) {
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

	draft := draftOf("Secondary package inspected [read#1 " + path + ":1].")
	report, grounding := assembleGroundedLedgerCloseout(roots, "implement_investigate", ev, nil, draft, nil, nil, 0)
	if len(report.CitedEvidence) != 1 || report.CitedEvidence[0].Path != path {
		t.Fatalf("cited evidence = %+v want secondary-root path", report.CitedEvidence)
	}
	if grounding == nil || !grounding.HostAssembled || len(grounding.CitedEvidence) != 1 {
		t.Fatalf("grounding = %+v want one host-assembled citation", grounding)
	}
	if openable := grounding.CitedEvidence[0].Openable; openable == nil || !*openable {
		t.Fatalf("secondary-root citation openable = %v want true", openable)
	}
}

func TestAssembleInvestigateCloseoutRetainsProseAndBindsObserved(t *testing.T) {
	msgs := []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{Name: "read", Args: map[string]any{"path": "incident/timeline.go"}}}},
		{Role: api.MessageRoleTool, Content: `{"path":"incident/timeline.go","content":"1|package incident","offset":1,"end_line":1,"limit":1}`},
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{Name: "web_search", Args: map[string]any{"query": "incident"}}}},
		{Role: api.MessageRoleTool, Content: `{"results":[{"url":"https://status.example.com/incident/42"}],"provider":"brave"}`},
	}
	ev := guidance.CloseoutEvidence{Ledger: ledgertest.BuildFromMessages("", msgs)}
	prose := "The outage began when the cache warmer looped; root cause is a missing backoff."

	report, grounding := assembleGroundedLedgerCloseout(evidence.CitationRoots{}, "implement_investigate", ev, nil, draftOf(prose), nil, []string{"INVEST_CITATIONS_REQUIRED"}, 3)

	if !strings.Contains(report.Synthesis, prose) {
		t.Fatalf("investigate closeout must retain model prose verbatim; got %q", report.Synthesis)
	}
	if grounding == nil || !grounding.HostAssembled {
		t.Fatal("investigate grounding must be host_assembled")
	}
	if grounding.Traced {
		t.Fatal("investigate observed-autobind exit is advisory → traced=false")
	}
	if grounding.HintCode != "INVEST_CITATIONS_REQUIRED" {
		t.Fatalf("hint_code=%q want INVEST_CITATIONS_REQUIRED", grounding.HintCode)
	}
	if grounding.RetryCount != 3 {
		t.Fatalf("retry_count=%d want 3", grounding.RetryCount)
	}
	if report.Synthesis != prose {
		t.Fatalf("investigate body must be prose only (no host advisory suffix); got %q", report.Synthesis)
	}
	if strings.Contains(report.Synthesis, "Reason code:") {
		t.Fatalf("reason code belongs on grounding.hint_code, not synthesis body: %q", report.Synthesis)
	}
	observedPaths := map[string]struct{}{}
	for _, p := range evidence.ObservedPathsSorted(ev.Ledger) {
		observedPaths[p] = struct{}{}
	}
	for _, c := range report.CitedEvidence {
		if _, ok := observedPaths[c.Path]; !ok {
			t.Fatalf("cited path %q not a subset of observed %v", c.Path, evidence.ObservedPathsSorted(ev.Ledger))
		}
	}
	observedURLs := map[string]struct{}{}
	for _, u := range evidence.ObservedURLsSorted(ev.Ledger) {
		observedURLs[u] = struct{}{}
	}
	for _, u := range report.CitedURLs {
		if _, ok := observedURLs[u]; !ok {
			t.Fatalf("cited url %q not a subset of observed %v", u, evidence.ObservedURLsSorted(ev.Ledger))
		}
	}
}

func TestAssembleInvestigateIncidentShapeBoundsCitedSet(t *testing.T) {
	var msgs []api.Message
	const paths = 34
	const urls = 40
	for i := 0; i < paths; i++ {
		p := fmt.Sprintf("incident/mod%02d/handler.go", i)
		msgs = append(msgs,
			api.Message{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{Name: "read", Args: map[string]any{"path": p}}}},
			api.Message{Role: api.MessageRoleTool, Content: fmt.Sprintf(`{"path":%q,"content":"1|package incident","offset":1,"end_line":1,"limit":1}`, p)},
		)
	}
	var results []string
	for i := 0; i < urls; i++ {
		results = append(results, fmt.Sprintf(`{"url":"https://status.example.com/incident/%02d"}`, i))
	}
	msgs = append(msgs,
		api.Message{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{Name: "web_search", Args: map[string]any{"query": "incident"}}}},
		api.Message{Role: api.MessageRoleTool, Content: `{"results":[` + strings.Join(results, ",") + `],"provider":"brave"}`},
	)
	ev := guidance.CloseoutEvidence{Ledger: ledgertest.BuildFromMessages("", msgs)}
	prose := "Root cause: a cache-warmer retry storm with no backoff cascaded across the fleet."

	report, grounding := assembleGroundedLedgerCloseout(evidence.CitationRoots{}, "implement_investigate", ev, nil, draftOf(prose), nil, []string{"INVEST_CITATIONS_REQUIRED"}, 3)

	if !strings.Contains(report.Synthesis, prose) {
		t.Fatalf("prose not retained: %q", report.Synthesis)
	}
	if grounding == nil || grounding.Traced || !grounding.HostAssembled {
		t.Fatalf("grounding = %+v want host_assembled traced=false", grounding)
	}
	if grounding.HintCode != "INVEST_CITATIONS_REQUIRED" {
		t.Fatalf("hint_code=%q want INVEST_CITATIONS_REQUIRED", grounding.HintCode)
	}
	if len(report.CitedEvidence) == 0 || len(report.CitedEvidence) > guidance.ObservedSampleCap {
		t.Fatalf("cited_evidence = %d, must be a bounded subset (<= %d)", len(report.CitedEvidence), guidance.ObservedSampleCap)
	}
	if len(report.CitedURLs) == 0 || len(report.CitedURLs) > guidance.ObservedSampleCap {
		t.Fatalf("cited_urls = %d, must be a bounded subset (<= %d)", len(report.CitedURLs), guidance.ObservedSampleCap)
	}
	if got := len(evidence.ObservedPathsSorted(ev.Ledger)); got <= guidance.ObservedSampleCap {
		t.Fatalf("fixture observed paths = %d, need > cap to prove bounding", got)
	}
}

func TestAssembleInvestigateCloseoutPrefersProsePathAnchors(t *testing.T) {
	var msgs []api.Message
	for i := 0; i < guidance.ObservedSampleCap+4; i++ {
		p := fmt.Sprintf("aaa/pad%02d.go", i)
		msgs = append(msgs,
			api.Message{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{Name: "read", Args: map[string]any{"path": p}}}},
			api.Message{Role: api.MessageRoleTool, Content: fmt.Sprintf(`{"path":%q,"content":"1|pad","offset":1,"end_line":1,"limit":1}`, p)},
		)
	}
	target := "lycaon/config/packs/painted-wolf/plan/policy/SPEC_POSTURE_RESEARCH_REQUIRED.yaml"
	msgs = append(msgs,
		api.Message{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{Name: "read", Args: map[string]any{"path": target}}}},
		api.Message{Role: api.MessageRoleTool, Content: fmt.Sprintf(
			`{"path":%q,"content":"20|Track","offset":20,"end_line":20,"limit":1}`, target)},
	)
	ev := guidance.CloseoutEvidence{Ledger: ledgertest.BuildFromMessages("", msgs)}
	prose := "Flagged `" + target + ":20` in shipped policy."

	report, grounding := assembleGroundedLedgerCloseout(evidence.CitationRoots{}, "implement_investigate", ev, nil, draftOf(prose), nil, []string{"INVEST_CITATIONS_REQUIRED"}, 1)
	if grounding == nil || !grounding.HostAssembled {
		t.Fatal("expected host_assembled grounding")
	}
	found := false
	for _, c := range grounding.CitedEvidence {
		if c.Path == target && c.Line == 20 {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("wire cited_evidence missing prose anchor %s:20; report=%v wire=%v",
			target, report.CitedEvidence, grounding.CitedEvidence)
	}
}

func TestAssembleInvestigateEmptyLedgerHonest(t *testing.T) {
	report, grounding := assembleGroundedLedgerCloseout(evidence.CitationRoots{}, "implement_investigate", guidance.CloseoutEvidence{Ledger: evidence.InitLedger()}, nil, guidance.CloseoutDraft{}, nil, []string{"INVEST_CITATIONS_REQUIRED"}, 3)

	if !strings.Contains(report.Synthesis, hostEmptyCloseoutSynthesis) {
		t.Fatalf("empty ledger must yield honest empty closeout, got %q", report.Synthesis)
	}
	if len(report.CitedEvidence) != 0 || len(report.CitedURLs) != 0 {
		t.Fatal("empty honest closeout must not fabricate citations")
	}
	if grounding != nil {
		t.Fatal("empty honest closeout carries no grounding envelope")
	}
}

// Protocol nodes never reach the assembled display, and a literal example does.
func TestAssembleLedgerCloseoutPresentsWithoutProtocolNodes(t *testing.T) {
	t.Parallel()
	msgs := []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{Name: "read", Args: map[string]any{"path": "greeting.py"}}}},
		{Role: api.MessageRoleTool, Content: `{"path":"greeting.py","content":"4|def format_greeting(name):","offset":4,"end_line":4,"limit":1}`},
	}
	ev := guidance.CloseoutEvidence{Ledger: ledgertest.BuildFromMessages("", msgs)}
	draft := draftOf("Design note written [read#1:4] [missing#9], see `[read#1]`.\n[verify: inspection - Documentation only.]")
	report, grounding := assembleGroundedLedgerCloseout(evidence.CitationRoots{}, "implement_investigate", ev, nil, draft, nil, []string{"INVEST_CITATION_UNVERIFIABLE"}, 1)
	if report.Synthesis != "Design note written [greeting.py:4](greeting.py#L4), see `[read#1]`." {
		t.Fatalf("display = %q", report.Synthesis)
	}
	if grounding == nil || !grounding.HostAssembled || grounding.Traced || len(grounding.CitedEvidence) == 0 {
		t.Fatalf("expected separately assembled grounding, got %+v", grounding)
	}
	if grounding.Verification == nil || grounding.Verification.Method != "inspection" {
		t.Fatalf("verification = %+v", grounding.Verification)
	}
}

func TestHostAttachedCloseoutPreservesReportMetadata(t *testing.T) {
	ev := guidance.CloseoutEvidence{Ledger: evidence.AssembleLedger([]evidence.Record{{
		Handle: "read#1", Kind: "read", Shape: evidence.ShapeFileRegion, Path: "README.md",
		Body: []string{"1|Project overview"}, LineRanges: []evidence.LineRange{{Start: 1, End: 1}},
	}})}
	const answer = "The project provides a desktop application."
	const artifact = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	read := guidance.ReadCloseoutReport(answer+"\n[verify: inspection - Read the architecture.]\n[artifact:"+artifact+"]\n\n```json\n"+
		`{"headline":"Project overview","summary":"Architecture inspected.","limits":["Runtime behavior not exercised."],"findings":[{"title":"Desktop application","impact":"Runs locally"}]}`+"\n```",
		guidance.RetainedCloseout{}, true, citationmd.Options{})
	report, grounding := assembleGroundedLedgerCloseout(evidence.CitationRoots{}, "implement_investigate", ev, nil, read.Draft, []string{artifact}, []string{guidance.InvestCitationsRequiredCode}, 0)
	if report.Synthesis != answer || report.Headline != "Project overview" || report.Summary != "Architecture inspected." || len(report.ArtifactIDs) != 1 || len(report.Findings) != 1 || len(report.Limits) != 1 {
		t.Fatalf("host attachment changed report metadata: %+v", report)
	}
	if grounding == nil || !grounding.HostAssembled || grounding.Traced || grounding.RetryCount != 0 || len(grounding.CitedEvidence) != 1 {
		t.Fatalf("host attachment did not expose observed sources immediately: %+v", grounding)
	}
	if grounding.Verification == nil || grounding.Verification.Method != verification.Inspection || grounding.Verification.Reason != "Read the architecture." {
		t.Fatalf("host attachment lost the reported validation scope: %+v", grounding)
	}
}
