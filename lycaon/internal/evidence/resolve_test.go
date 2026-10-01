package evidence_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance/ledgertest"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestResolve_matchedVerbatim(t *testing.T) {
	readJSON := `{"path":"f.go","content":"42|  return nil","offset":42,"end_line":42,"limit":1}`
	ev := ledgertest.BuildFromMessages("", []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "read", ID: "c1", Args: map[string]any{"path": "f.go", "offset": 42, "limit": 1}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted, Content: readJSON}},
	})

	got := evidence.Resolve(evidence.CitationRoots{}, evidence.Triple{
		Path: "f.go", Line: 42, Excerpt: "return nil",
	}, ev, "")
	if got.Verdict != evidence.VerdictMatched {
		t.Fatalf("verdict = %q want matched", got.Verdict)
	}
	if got.Handle != "read#1" {
		t.Fatalf("handle = %q want read#1", got.Handle)
	}
}

func TestResolve_matchedWhitespaceReflow(t *testing.T) {
	readJSON := `{"path":"a.tsx","content":"35\t    if (el.innerHTML !== html) {\n36\t      el.innerHTML = html;\n37\t    }\n","offset":35,"end_line":37,"limit":3}`
	ev := ledgertest.BuildFromMessages("", []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "read", ID: "c1", Args: map[string]any{"path": "a.tsx", "offset": 35, "limit": 3}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted, Content: readJSON}},
	})
	reflowed := "if (el.innerHTML !== html) {\n el.innerHTML = html;\n }"

	got := evidence.Resolve(evidence.CitationRoots{}, evidence.Triple{
		Path: "a.tsx", Line: 35, Excerpt: reflowed,
	}, ev, "")
	if got.Verdict != evidence.VerdictMatched {
		t.Fatalf("verdict = %q want matched", got.Verdict)
	}
}

func TestResolve_tracedParaphrase(t *testing.T) {
	readJSON := `{"path":"f.go","content":"42|  return nil","offset":42,"end_line":42,"limit":1}`
	ev := ledgertest.BuildFromMessages("", []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "read", ID: "c1", Args: map[string]any{"path": "f.go", "offset": 42, "limit": 1}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted, Content: readJSON}},
	})

	got := evidence.Resolve(evidence.CitationRoots{}, evidence.Triple{
		Path: "f.go", Line: 42, Excerpt: "returns nil on success",
	}, ev, "")
	if got.Verdict != evidence.VerdictTraced {
		t.Fatalf("verdict = %q want traced", got.Verdict)
	}
	if got.Handle != "read#1" {
		t.Fatalf("handle = %q want read#1", got.Handle)
	}
}

func TestResolve_tracedWrongLine(t *testing.T) {
	readJSON := `{"path":"f.go","content":"42|  return nil\n43|  // tail","offset":42,"end_line":43,"limit":2}`
	ev := ledgertest.BuildFromMessages("", []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "read", ID: "c1", Args: map[string]any{"path": "f.go", "offset": 42, "limit": 2}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted, Content: readJSON}},
	})

	got := evidence.Resolve(evidence.CitationRoots{}, evidence.Triple{
		Path: "f.go", Line: 99, Excerpt: "return nil",
	}, ev, "")
	if got.Verdict != evidence.VerdictTraced {
		t.Fatalf("verdict = %q want traced", got.Verdict)
	}
	if got.Handle == "" {
		t.Fatal("expected best-effort handle mint for traced citation")
	}
}

func TestResolve_unverifiableNeverReadPath(t *testing.T) {
	ev := ledgertest.BuildFromMessages("", []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "read", ID: "c1", Args: map[string]any{"path": "seen.go"}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted, Content: "ok"}},
	})

	got := evidence.Resolve(evidence.CitationRoots{}, evidence.Triple{
		Path: "never.go", Line: 1, Excerpt: "package never",
	}, ev, "")
	if got.Verdict != evidence.VerdictUnverifiable {
		t.Fatalf("verdict = %q want unverifiable", got.Verdict)
	}
	if got.Handle != "" {
		t.Fatalf("handle = %q want empty", got.Handle)
	}
}

func TestResolve_abuseGuardContentFromOtherPath(t *testing.T) {
	ev := ledgertest.BuildFromMessages("", []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "read", ID: "c1", Args: map[string]any{"path": "a.go"}},
			{Name: "read", ID: "c2", Args: map[string]any{"path": "b.go"}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted, Content: `{"path":"a.go","content":"1\tpackage a\n","offset":1,"end_line":1}`}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted, Content: `{"path":"b.go","content":"1\tpackage b\n","offset":1,"end_line":1}`}},
	})

	got := evidence.Resolve(evidence.CitationRoots{}, evidence.Triple{
		Path: "b.go", Line: 1, Excerpt: "package a",
	}, ev, "")
	if got.Verdict != evidence.VerdictUnverifiable {
		t.Fatalf("verdict = %q want unverifiable (abuse guard)", got.Verdict)
	}
	if got.Handle != "" {
		t.Fatalf("handle = %q want empty", got.Handle)
	}
}

func TestResolve_handleHintNarrowsMint(t *testing.T) {
	ev := ledgertest.BuildFromMessages("", []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "read", ID: "c1", Args: map[string]any{"path": "f.go", "offset": 1, "limit": 1}},
			{Name: "read", ID: "c2", Args: map[string]any{"path": "f.go", "offset": 10, "limit": 1}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted, Content: `{"path":"f.go","content":"1| alpha","offset":1,"end_line":1,"limit":1}`}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted, Content: `{"path":"f.go","content":"10| beta","offset":10,"end_line":10,"limit":1}`}},
	})

	got := evidence.Resolve(evidence.CitationRoots{}, evidence.Triple{
		Path: "f.go", Line: 10, Excerpt: "paraphrased beta",
	}, ev, "read#1")
	if got.Verdict != evidence.VerdictTraced {
		t.Fatalf("verdict = %q want traced", got.Verdict)
	}
	if got.Handle != "read#1" {
		t.Fatalf("handle = %q want read#1 from hint", got.Handle)
	}
}

func TestResolve_handleHintIgnoredWhenInvalid(t *testing.T) {
	ev := ledgertest.BuildFromMessages("", []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "read", ID: "c1", Args: map[string]any{"path": "f.go", "offset": 10, "limit": 1}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted, Content: `{"path":"f.go","content":"10| beta","offset":10,"end_line":10,"limit":1}`}},
	})

	got := evidence.Resolve(evidence.CitationRoots{}, evidence.Triple{
		Path: "f.go", Line: 10, Excerpt: "paraphrased beta",
	}, ev, "read#99")
	if got.Verdict != evidence.VerdictTraced {
		t.Fatalf("verdict = %q want traced", got.Verdict)
	}
	if got.Handle != "read#1" {
		t.Fatalf("handle = %q want read#1 from newest live handle", got.Handle)
	}
}
