package evidence_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/guidance/ledgertest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestBuildLedgerFromTranscriptUsesOpaqueUndeclaredMCPShape(t *testing.T) {
	content := "Observation payload with enough span for grounding checks here."
	ev := ledgertest.BuildFromMessages("", []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "mcp_custom_fetch", ID: "c1", Args: map[string]any{}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted, Content: content}},
	})
	handle := evidence.MCPServerKindPrefix + "customfetch#1"
	rec, ok := evidence.ResolveHandle(ev, handle)
	if !ok {
		t.Fatalf("handles = %v", evidence.HandlesSorted(ev))
	}
	if rec.Shape != evidence.ShapeOpaque {
		t.Fatalf("shape = %q want opaque", rec.Shape)
	}
	if rec.Kind != evidence.MCPServerKindPrefix+"customfetch" {
		t.Fatalf("kind = %q", rec.Kind)
	}
	if !evidence.ExcerptMatchesHandle(ev, handle, 0, "Observation payload") {
		t.Fatal("expected verbatim excerpt to verify")
	}
	if evidence.ExcerptMatchesHandle(ev, handle, 0, "fabricated text") {
		t.Fatal("expected fabricated excerpt to fail")
	}
}

func TestBuildLedgerFromTranscript_nativeScanQuery(t *testing.T) {
	flagged := "internal/auth/handler.go"
	body := `{"scan_id":"scan-1","findings":[{"rule_id":"r1","level":"high","message":"SQL injection risk","locations":[{"uri":"` + flagged + `","start_line":42}]}],"total_match":1}`
	ev := ledgertest.BuildFromMessages("", []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "scan_query", ID: "c1", Args: map[string]any{"scan_id": "scan-1"}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{
			Outcome: api.ToolResultOutcomeCompleted,
			Content: body,
		}},
	})
	rec, ok := evidence.ResolveHandle(ev, "scan#1")
	if !ok {
		t.Fatalf("handles = %v", evidence.HandlesSorted(ev))
	}
	if rec.Kind != "scan" || rec.Shape != evidence.ShapeArtifact {
		t.Fatalf("rec = %+v want scan/artifact", rec)
	}
	if rec.Fidelity != evidence.FidelityStructured {
		t.Fatalf("trust = %q want structured", rec.Fidelity)
	}
	if !evidence.PathObserved(ev, flagged) {
		t.Fatalf("expected %q in evidence, got %v", flagged, evidence.ObservedPathsSorted(ev))
	}
	if ok, _ := evidence.VerifyRecord(rec, evidence.Claim{Excerpt: "SQL injection risk"}); !ok {
		t.Fatal("finding message must ground against captured scan MCP body")
	}
}

func TestBuildLedgerFromTranscript_enrichedMCPShape(t *testing.T) {
	evidence.SetBinding(nil)
	b, err := evidence.LoadBinding()
	testutil.FailErr(t, "evidence.LoadBinding failed", err)
	testutil.FailErr(t, "replace MCP declarations", b.ReplaceMCPToolDeclarations([]evidence.MCPToolDeclaration{{
		ToolName: "mcp_demo_tool", Kind: "scanfindings", Shape: evidence.ShapeArtifact,
	}}))
	evidence.SetBinding(b)
	t.Cleanup(func() { evidence.SetBinding(nil) })

	ev := ledgertest.BuildFromMessages("", []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "mcp_demo_tool", ID: "c1", Args: map[string]any{}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted, Content: `{"findings":[{"id":"x"}]}`}},
	})
	rec, ok := evidence.ResolveHandle(ev, evidence.MCPServerKindPrefix+"scanfindings#1")
	if !ok || rec.Shape != evidence.ShapeArtifact {
		t.Fatalf("rec = %+v ok=%v", rec, ok)
	}
}

func TestRecordsForWire_trustTier(t *testing.T) {
	ev := ledgertest.BuildFromMessages("", []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "mcp_custom_fetch", ID: "c1", Args: map[string]any{}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted, Content: strings.Repeat("x", evidence.EvidenceCaptureBodyCapBytes+10)}},
	})
	var g api.CitationGrounding
	guidance.StampEvidenceRecords(&g, ev)
	records := g.EvidenceRecords
	if len(records) != 1 {
		t.Fatalf("records = %+v", records)
	}
	if records[0].Fidelity != evidence.FidelityOpaque {
		t.Fatalf("trust = %q", records[0].Fidelity)
	}
	if !records[0].Truncated {
		t.Fatal("expected truncated capture marker")
	}
}

func TestRecordsForWire_webSearchURLs(t *testing.T) {
	body := `{"results":[{"url":"https://a.example/1"},{"url":"https://b.example/2"}],"provider":"brave"}`
	ev := ledgertest.BuildFromMessages("", []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "web_search", ID: "c1", Args: map[string]any{"query": "x"}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted, Content: body}},
	})
	var g api.CitationGrounding
	guidance.StampEvidenceRecords(&g, ev)
	records := g.EvidenceRecords
	if len(records) != 1 {
		t.Fatalf("records = %+v", records)
	}
	got := records[0].URLs
	want := []string{"https://a.example/1", "https://b.example/2"}
	if len(got) != len(want) {
		t.Fatalf("wire URLs = %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("wire URLs = %v want %v", got, want)
		}
	}
}

// The records a grounding cites fill the capped sample first, so a long ledger
// of uncited calls never pushes them off the wire.
func TestRecordsForWire_citedRecordsLead(t *testing.T) {
	var msgs []api.Message
	for i := range 40 {
		id := "c" + strings.Repeat("x", i)
		msgs = append(msgs,
			api.Message{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{Name: "web_search", ID: id, Args: map[string]any{"query": id}}}},
			api.Message{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted, Content: `{"results":[{"url":"https://a.example/` + id + `"}]}`}},
		)
	}
	ev := ledgertest.BuildFromMessages("", msgs)
	handles := evidence.HandlesSorted(ev)
	if len(handles) < 30 {
		t.Fatalf("handles = %d, want a ledger past the wire cap", len(handles))
	}
	cited := handles[len(handles)-1]
	g := api.CitationGrounding{CitedEvidence: []api.CitationGroundingCitedEvidence{{Handle: cited}}}
	guidance.StampEvidenceRecords(&g, ev)
	if len(g.EvidenceRecords) == 0 || g.EvidenceRecords[0].Handle != cited {
		t.Fatalf("first record = %+v, want the cited %s", g.EvidenceRecords, cited)
	}
	if g.EvidenceRecordCount != len(handles) {
		t.Fatalf("count = %d, want the whole ledger %d", g.EvidenceRecordCount, len(handles))
	}
}
