package evidence_test

import (
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance/ledgertest"
	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestShapeRegistryRegistersAllShapes(t *testing.T) {
	reg := evidence.DefaultShapeRegistry()
	for _, id := range evidence.ShapeIDs() {
		if _, ok := reg.Lookup(id); !ok {
			t.Fatalf("missing shape %q in registry", id)
		}
	}
}

func TestShapeRegistryUnknownShapeNotVerified(t *testing.T) {
	rec := evidence.Record{Kind: "read", Shape: "unknown_shape"}
	ok, reason := evidence.VerifyRecord(rec, evidence.Claim{Excerpt: "anything long enough"})
	if ok {
		t.Fatal("unknown shape must not verify")
	}
	if reason != "" {
		t.Fatalf("reason = %q want empty", reason)
	}
}

func TestBuildLedgerFromTranscriptStampsShape(t *testing.T) {
	ev := ledgertest.BuildFromMessages("", []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "read", ID: "c1", Args: map[string]any{"path": "a.go"}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted, Content: "x"}},
	})
	rec, ok := evidence.ResolveHandle(ev, "read#1")
	if !ok || rec.Shape != evidence.ShapeFileRegion {
		t.Fatalf("rec = %+v ok=%v", rec, ok)
	}
}

func TestOperationalToolsProduceFirstClassStructuredEvidence(t *testing.T) {
	tests := []struct {
		tool, handle, shape, surface string
	}{
		{tool: "secret_generate", handle: "secret_lifecycle#1", shape: evidence.ShapeStructuredEvent},
		{tool: "http_request", handle: "http_response#1", shape: evidence.ShapeSurfaceSnapshot, surface: evidence.SurfaceHTTP},
		{tool: "terminal_send", handle: "terminal_session#1", shape: evidence.ShapeSurfaceSnapshot, surface: evidence.SurfaceTUI},
	}
	for _, tc := range tests {
		t.Run(tc.tool, func(t *testing.T) {
			body := `{"outcome":"accepted","value_disclosed":false}`
			ev := ledgertest.BuildFromMessages("", []api.Message{
				{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{Name: tc.tool, ID: "c1", Args: map[string]any{}}}},
				{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted, Content: body}},
			})
			rec, ok := evidence.ResolveHandle(ev, tc.handle)
			if !ok || rec.Shape != tc.shape || rec.Surface != tc.surface || rec.Fidelity != evidence.FidelityStructured {
				t.Fatalf("record = %+v ok=%v", rec, ok)
			}
			if !evidence.ExcerptMatchesHandle(ev, tc.handle, "", 0, "value_disclosed") {
				t.Fatal("structured result body must support a typed excerpt citation")
			}
		})
	}
}

func TestVerbatimTrivialityFloor(t *testing.T) {
	ev := ledgertest.BuildFromMessages("", []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "read", ID: "c1", Args: map[string]any{"path": "f.go"}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{
			Outcome: api.ToolResultOutcomeCompleted,
			Content: fmt.Sprintf(`{"path":"f.go","content":%q,"offset":1,"end_line":1,"limit":1}`, hostmarker.FormatNumberedLines([]string{"if x"}, 1)),
		}},
	})
	if evidence.ExcerptMatchesHandle(ev, "read#1", "f.go", 1, "if") {
		t.Fatal("sub-min-span excerpt must not verify")
	}
}

func TestFileRegionNormalizerFoldsSeparators(t *testing.T) {
	shape, ok := evidence.DefaultShapeRegistry().Lookup(evidence.ShapeFileRegion)
	if !ok {
		t.Fatal("missing file_region shape")
	}
	got := shape.Normalizer.Normalize(`.\foo\bar.go`)
	if got != "foo/bar.go" {
		t.Fatalf("Normalize = %q want foo/bar.go", got)
	}
}

func TestVerifyURLObserved(t *testing.T) {
	body := `{"url":"https://docs.example.com/a","content":"ok"}`
	ev := ledgertest.BuildFromMessages("", []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "fetch_url", ID: "c1", Args: map[string]any{"url": "https://docs.example.com/a"}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted, Content: body}},
	})
	if !evidence.VerifyURLObserved(ev, "https://docs.example.com/a") {
		t.Fatal("expected url observed")
	}
	if evidence.VerifyURLObserved(ev, "https://docs.example.com/b") {
		t.Fatal("expected url not observed")
	}
}

func TestVerifyURLObservedForOpaqueMCPURL(t *testing.T) {
	want := "https://docs.example.com/from-mcp"
	ev := ledgertest.BuildFromMessages("", []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "mcp_custom_fetch", ID: "c1", Args: map[string]any{}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{
			Outcome: api.ToolResultOutcomeCompleted,
			Content: `{"result":{"url":"` + want + `"}}`,
		}},
	})
	rec, ok := evidence.ResolveHandle(ev, evidence.MCPServerKindPrefix+"customfetch#1")
	if !ok || rec.Shape != evidence.ShapeOpaque {
		t.Fatalf("opaque MCP record = %+v ok=%v", rec, ok)
	}
	if !evidence.VerifyURLObserved(ev, want) {
		t.Fatalf("MCP URL %q must share host and agent observation eligibility", want)
	}
}

func TestVerifyURLObservedIncludesEverySearchResult(t *testing.T) {
	body := `{"results":[` +
		`{"url":"https://github.com/cowrie/cowrie","title":"Cowrie"},` +
		`{"url":"https://medium.com/@fa.ouardirhi/cowrie-honeypot-in-action","title":"In Action"},` +
		`{"url":"https://docs.cowrie.org/","title":"Docs"}` +
		`],"provider":"brave"}`
	ev := ledgertest.BuildFromMessages("", []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "web_search", ID: "c1", Args: map[string]any{"query": "shell simulator uses"}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted, Content: body}},
	})
	for _, want := range []string{
		"https://github.com/cowrie/cowrie",
		"https://medium.com/@fa.ouardirhi/cowrie-honeypot-in-action",
		"https://docs.cowrie.org/",
	} {
		if !evidence.VerifyURLObserved(ev, want) {
			t.Fatalf("expected search result %q observed", want)
		}
	}
	if evidence.VerifyURLObserved(ev, "https://example.com/never-returned") {
		t.Fatal("a URL absent from the result body must not ground")
	}
}

func TestVerifyURLObservedIgnoresFetchBodyLinks(t *testing.T) {
	body := `{"url":"https://example.com/page","content":"see also https://example.com/other and https://third.example/x"}`
	ev := ledgertest.BuildFromMessages("", []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "fetch_url", ID: "c1", Args: map[string]any{"url": "https://example.com/page"}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted, Content: body}},
	})
	if !evidence.VerifyURLObserved(ev, "https://example.com/page") {
		t.Fatal("the fetched page URL must ground")
	}
	for _, href := range []string{"https://example.com/other", "https://third.example/x"} {
		if evidence.VerifyURLObserved(ev, href) {
			t.Fatalf("incidental body href %q must not ground from a fetch_url", href)
		}
	}
}
