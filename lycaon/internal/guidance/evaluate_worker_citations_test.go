package guidance_test

import (
	"github.com/lycaon/lycaon/internal/evidence"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/guidance/ledgertest"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestEvaluateWorkerCitations_barePathFindingBlocks(t *testing.T) {
	ev := ledgertest.BuildFromMessages("", nil)
	eval := guidance.EvaluateWorkerCitations(evidence.CitationRoots{}, []guidance.WorkerFindingInput{
		{Path: "internal/foo.go"},
	}, nil, guidance.WorkerNarrativeInput{}, ev)
	if eval.Code != guidance.WorkerEvidenceHandleUnknownCode {
		t.Fatalf("eval = %+v want bare path finding blocked", eval)
	}
}

func TestEvaluateWorkerCitations_groundedFinding(t *testing.T) {
	readJSON := `{"path":"f.go","content":"42|  return nil","offset":42,"end_line":42,"limit":1}`
	msgs := []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "read", ID: "c1", Args: map[string]any{"path": "f.go", "offset": 42, "limit": 1}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted, Content: readJSON}},
	}
	ev := ledgertest.BuildFromMessages("", msgs)
	eval := guidance.EvaluateWorkerCitations(evidence.CitationRoots{}, []guidance.WorkerFindingInput{
		{Path: "f.go", Evidence: "read#1", Line: 42, Excerpt: "return nil"},
	}, nil, guidance.WorkerNarrativeInput{}, ev)
	if eval.Code != "" {
		t.Fatalf("eval = %+v want pass", eval)
	}
}

func TestEvaluateWorkerCitations_unknownHandle(t *testing.T) {
	ev := ledgertest.BuildFromMessages("", nil)
	eval := guidance.EvaluateWorkerCitations(evidence.CitationRoots{}, []guidance.WorkerFindingInput{
		{Path: "never.go", Line: 1, Excerpt: "package never"},
	}, nil, guidance.WorkerNarrativeInput{}, ev)
	if eval.Code != guidance.WorkerEvidenceHandleUnknownCode {
		t.Fatalf("eval = %+v", eval)
	}
}

func TestEvaluateWorkerCitations_excerptMismatch(t *testing.T) {
	readJSON := `{"path":"f.go","content":"42|  return nil","offset":42,"end_line":42,"limit":1}`
	msgs := []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "read", ID: "c1", Args: map[string]any{"path": "f.go", "offset": 42, "limit": 1}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted, Content: readJSON}},
	}
	ev := ledgertest.BuildFromMessages("", msgs)
	eval := guidance.EvaluateWorkerCitations(evidence.CitationRoots{}, []guidance.WorkerFindingInput{
		{Path: "f.go", Evidence: "read#1", Line: 42, Excerpt: "return 1"},
	}, nil, guidance.WorkerNarrativeInput{}, ev)
	if eval.Code != "" {
		t.Fatalf("eval = %+v want non-blocking traced", eval)
	}
	if len(eval.Resolutions) != 1 || eval.Resolutions[0].Verdict != evidence.VerdictTraced {
		t.Fatalf("resolved = %+v want traced", eval.Resolutions)
	}
}

// mcpEvidence builds a ledger from one user-added MCP tool call whose opaque body
// grounds by verbatim substring, for any worker agent type.
func mcpEvidence(t *testing.T) (evidence.Ledger, string) {
	t.Helper()
	const body = `{"finding":"hardcoded token in config","severity":"high"}`
	msgs := []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "mcp_acme_scan_query", ID: "c1", Args: map[string]any{"q": "tokens"}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted, Content: body}},
	}
	ev := ledgertest.BuildFromMessages("", msgs)
	handles := evidence.HandlesSorted(ev)
	if len(handles) == 0 {
		t.Fatal("expected an MCP handle for mcp_acme_scan_query")
	}
	return ev, handles[0]
}

func TestEvaluateWorkerCitations_mcpOpaqueGrounded(t *testing.T) {
	ev, handle := mcpEvidence(t)
	eval := guidance.EvaluateWorkerCitations(evidence.CitationRoots{}, []guidance.WorkerFindingInput{
		{Evidence: handle, Excerpt: "hardcoded token in config"},
	}, nil, guidance.WorkerNarrativeInput{}, ev)
	if eval.Code != "" {
		t.Fatalf("eval = %+v want pass for verbatim MCP excerpt", eval)
	}
}

func TestEvaluateWorkerCitations_mcpOpaqueExcerptMismatch(t *testing.T) {
	ev, handle := mcpEvidence(t)
	eval := guidance.EvaluateWorkerCitations(evidence.CitationRoots{}, []guidance.WorkerFindingInput{
		{Evidence: handle, Excerpt: "fabricated finding never returned by the tool"},
	}, nil, guidance.WorkerNarrativeInput{}, ev)
	if eval.Code != "" {
		t.Fatalf("eval = %+v want non-blocking traced", eval)
	}
	if len(eval.Resolutions) != 1 || eval.Resolutions[0].Verdict != evidence.VerdictTraced {
		t.Fatalf("resolved = %+v want traced", eval.Resolutions)
	}
}

func TestEvaluateWorkerCitations_urlNotObserved(t *testing.T) {
	body := `{"url":"https://docs.example.com/a","content":"ok"}`
	msgs := []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "fetch_url", ID: "c1", Args: map[string]any{"url": "https://docs.example.com/a"}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted, Content: body}},
	}
	ev := ledgertest.BuildFromMessages("", msgs)
	eval := guidance.EvaluateWorkerCitations(evidence.CitationRoots{}, nil, []string{"https://docs.example.com/b"}, guidance.WorkerNarrativeInput{}, ev)
	if eval.Code != guidance.WorkerURLNotObservedCode {
		t.Fatalf("eval = %+v", eval)
	}
}

func TestGroundingHintData_observedPaths(t *testing.T) {
	ev := evidenceWithObservedPaths("a.go", "b.go", "c.go")
	data := guidance.GroundingHintData(nil, ev)
	if data["observed_paths_count"] != 3 {
		t.Fatalf("data = %+v", data)
	}
	if data["observed_paths_sample"] == "" {
		t.Fatal("expected observed_paths_sample")
	}
	if data["observed_handles_count"] != 3 || data["observed_handles_sample"] == "" {
		t.Fatalf("expected exact observed handles, data = %+v", data)
	}
}

func TestGroundingHintData_observedURLs(t *testing.T) {
	body := `{"url":"https://docs.example.com/a","content":"ok"}`
	msgs := []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "fetch_url", ID: "c1", Args: map[string]any{"url": "https://docs.example.com/a"}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted, Content: body}},
	}
	ev := ledgertest.BuildFromMessages("", msgs)
	data := guidance.GroundingHintData(nil, ev)
	if data["observed_urls_count"] != 1 {
		t.Fatalf("data = %+v", data)
	}
	if data["observed_urls_sample"] == "" {
		t.Fatal("expected observed_urls_sample")
	}
}

func TestLedgerHasSurveyHandle(t *testing.T) {
	msgs := []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "grep", ID: "c1", Args: map[string]any{"path": ".", "pattern": "x"}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted, Content: `{}`}},
	}
	ev := ledgertest.BuildFromMessages("", msgs)
	if !guidance.LedgerHasSurveyHandle(ev) {
		t.Fatal("expected survey handle")
	}
}

func TestEvaluateWorkerCitations_visualIntentIsSurfaceUngrounded(t *testing.T) {
	msgs := []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "render_view", ID: "c1", Args: map[string]any{"markup": "<div/>"}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted, Content: `{"mime":"image/png"}`}},
	}
	ev := ledgertest.BuildFromMessages("", msgs)
	eval := guidance.EvaluateWorkerCitations(evidence.CitationRoots{}, []guidance.WorkerFindingInput{
		{Evidence: "render#1", Line: 1, Excerpt: "image/png"},
	}, nil, guidance.WorkerNarrativeInput{}, ev)
	if eval.Code != guidance.SurfaceClaimUngroundedCode {
		t.Fatalf("eval = %+v want %s", eval, guidance.SurfaceClaimUngroundedCode)
	}
}

func TestEvaluateWorkerCitations_pageMeasureUngrounded(t *testing.T) {
	page := evidence.BuildEvidenceRecord("/tmp/proj", "capture_page", map[string]any{"url": "http://x"}, `{"log":["ok"],"snapshot":{"role":"document"}}`)
	ev := evidence.Ledger{Handles: map[string]evidence.Record{"page#1": page}}
	eval := guidance.EvaluateWorkerCitations(evidence.CitationRoots{}, []guidance.WorkerFindingInput{
		{Evidence: "page#1", Line: 1, Excerpt: `"width": 120`},
	}, nil, guidance.WorkerNarrativeInput{}, ev)
	if eval.Code != guidance.PageMeasureUngroundedCode {
		t.Fatalf("eval = %+v want %s", eval, guidance.PageMeasureUngroundedCode)
	}

	geom := evidence.BuildEvidenceRecord("/tmp/proj", "measure_page", map[string]any{"selectors": []any{"#box-a"}}, `{"elements":[{"selector":"#box-a","rect":{"width":120}}]}`)
	ev2 := evidence.Ledger{Handles: map[string]evidence.Record{
		"page#1":          page,
		"page_geometry#1": geom,
	}}
	eval = guidance.EvaluateWorkerCitations(evidence.CitationRoots{}, []guidance.WorkerFindingInput{
		{Evidence: "page_geometry#1", Line: 1, Excerpt: `"width":120`},
	}, nil, guidance.WorkerNarrativeInput{}, ev2)
	if eval.Code != "" {
		t.Fatalf("geometry cite should ground; got %+v", eval)
	}
}

func TestEvaluateSurfaceClaimComplete_renderWithoutPage(t *testing.T) {
	msgs := []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "render_view", ID: "c1", Args: map[string]any{"markup": "<div/>"}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted, Content: `{"mime":"image/png"}`}},
	}
	ev := ledgertest.BuildFromMessages("", msgs)
	code, offenders := guidance.EvaluateSurfaceClaimComplete("complete", ev)
	if code != guidance.SurfaceClaimUngroundedCode {
		t.Fatalf("code = %q want %s", code, guidance.SurfaceClaimUngroundedCode)
	}
	if len(offenders) == 0 {
		t.Fatal("expected render handle offenders")
	}
	code, _ = guidance.EvaluateSurfaceClaimComplete("partial", ev)
	if code != "" {
		t.Fatalf("partial legs must not block; got %q", code)
	}
}

func TestEvaluateSurfaceClaimComplete_pageSatisfies(t *testing.T) {
	msgs := []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "render_view", ID: "c1", Args: map[string]any{"markup": "<div/>"}},
			{Name: "capture_page", ID: "c2", Args: map[string]any{"url": "http://127.0.0.1/"}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted, Content: `{"mime":"image/png"}`}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted, Content: `{"state":{},"snapshot":{},"log":[]}`}},
	}
	ev := ledgertest.BuildFromMessages("", msgs)
	code, _ := guidance.EvaluateSurfaceClaimComplete("complete", ev)
	if code != "" {
		t.Fatalf("page capture should satisfy; got %q", code)
	}
}

func TestEvaluateSurfaceClaimComplete_tuiSatisfies(t *testing.T) {
	render := evidence.BuildEvidenceRecord("/tmp/proj", "render_view", nil, `{"ok":true}`)
	tui := evidence.BuildEvidenceRecord("/tmp/proj", "terminal_snapshot", map[string]any{"id": "pty-1"},
		`{"surface":"tui","state":{},"snapshot":{"lines":["TITLE"]},"log":[]}`)
	ev := evidence.Ledger{Handles: map[string]evidence.Record{
		"render#1": render,
		"tui#1":    tui,
	}}
	code, _ := guidance.EvaluateSurfaceClaimComplete("complete", ev)
	if code != "" {
		t.Fatalf("tui surface_snapshot should satisfy; got %q", code)
	}
}

func TestEvaluateSurfaceClaimComplete_sealedCommandTerminalCaptureSatisfies(t *testing.T) {
	msgs := []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "command", ID: "c1", Args: map[string]any{
				"command":          "./ntphealth -timeout 5s",
				"terminal_capture": map[string]any{},
			}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{
			Outcome: api.ToolResultOutcomeCompleted,
			Content: `{"ok":true,"terminal_capture":{"surface":"tui","state":{},"snapshot":{"lines":["4 succeeded"]}}}`,
		}},
	}
	ev := ledgertest.BuildFromMessages("", msgs)
	if _, ok := evidence.ResolveHandle(ev, "tui#1"); !ok {
		t.Fatalf("sealed command capture did not mint tui evidence: %v", evidence.HandlesSorted(ev))
	}
	code, _ := guidance.EvaluateSurfaceClaimComplete("complete", ev)
	if code != "" {
		t.Fatalf("sealed command terminal capture should satisfy; got %q", code)
	}
}

func TestPrependHandleTag(t *testing.T) {
	got := guidance.PrependHandleTag("body", "read#1")
	if got != "[read#1]\nbody" {
		t.Fatalf("got %q", got)
	}
}

// A layout number absent from the cited page capture is PAGE_MEASURE_UNGROUNDED
// even when unrelated page_geometry records exist elsewhere in the session.
func TestEvaluateWorkerCitations_pageMeasureFabricatedWithGeometryPresent(t *testing.T) {
	page := evidence.BuildEvidenceRecord("/tmp/proj", "capture_page", map[string]any{"url": "http://x"}, `{"log":["ok"],"snapshot":{"role":"document"}}`)
	geom := evidence.BuildEvidenceRecord("/tmp/proj", "measure_page", map[string]any{"selectors": []any{"#box-a"}}, `{"elements":[{"selector":"#box-a","rect":{"width":120}}]}`)
	ev := evidence.Ledger{Handles: map[string]evidence.Record{
		"page#1":          page,
		"page_geometry#1": geom,
	}}
	// 132 is in neither record: the worker read it off the screenshot.
	eval := guidance.EvaluateWorkerCitations(evidence.CitationRoots{}, []guidance.WorkerFindingInput{
		{Evidence: "page#1", Line: 1, Excerpt: `"width": 132`},
	}, nil, guidance.WorkerNarrativeInput{}, ev)
	if eval.Code != guidance.PageMeasureUngroundedCode {
		t.Fatalf("eval = %+v want %s for a figure present in no evidence record", eval, guidance.PageMeasureUngroundedCode)
	}
}
