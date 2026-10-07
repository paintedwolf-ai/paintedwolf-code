package evidence_test

import (
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance/ledgertest"
	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestResolve_matchedVerbatim(t *testing.T) {
	readJSON := fmt.Sprintf(`{"path":"f.go","content":%q,"offset":42,"end_line":42,"limit":1}`, hostmarker.FormatNumberedLines([]string{"return nil"}, 42))
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
	readJSON := fmt.Sprintf(`{"path":"a.tsx","content":%q,"offset":35,"end_line":37,"limit":3}`, hostmarker.FormatNumberedLines([]string{"    if (el.innerHTML !== html) {", "      el.innerHTML = html;", "    }"}, 35))
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
	readJSON := `{"path":"f.go","content":"42|  return nil\n43|  return nil","offset":42,"end_line":43,"limit":2}`
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

func TestResolveMatchesDirectoryGrepLineByCitedPath(t *testing.T) {
	grepJSON := `{"matches":[{"path":"lycaon/internal/confine/confine_test.go","line":895,"content":"func TestConfineDefault(t *testing.T) {"}]}`
	ev := ledgertest.BuildFromMessages("", []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "grep", ID: "c1", Args: map[string]any{"path": "lycaon/internal/confine", "pattern": "TestConfine"}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted, Content: grepJSON}},
	})

	got := evidence.Resolve(evidence.CitationRoots{}, evidence.Triple{
		Path: "lycaon/internal/confine/confine_test.go", Line: 895, Excerpt: "func TestConfineDefault",
	}, ev, "")
	if got.Verdict != evidence.VerdictMatched {
		t.Fatalf("verdict = %q want matched", got.Verdict)
	}
	if got.Handle != "grep#1" {
		t.Fatalf("handle = %q want grep#1", got.Handle)
	}
}

func TestResolveReanchorsUniqueVerbatimExcerpt(t *testing.T) {
	grepJSON := `{
		"matches": [{
			"path": "harness_control.go",
			"line": 33,
			"content": "target verbatim line to anchor",
			"context_before": ["line 31", "line 32"]
		}]
	}`
	ev := ledgertest.BuildFromMessages("", []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "grep", ID: "c1", Args: map[string]any{"path": "harness_control.go", "pattern": "target"}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted, Content: grepJSON}},
	})

	// Cited line 30, but true line is 33
	got := evidence.Resolve(evidence.CitationRoots{}, evidence.Triple{
		Path: "harness_control.go", Line: 30, Excerpt: "target verbatim line to anchor",
	}, ev, "")
	if got.Verdict != evidence.VerdictMatched {
		t.Fatalf("verdict = %q want matched", got.Verdict)
	}
	if got.Line != 33 {
		t.Fatalf("line = %d want 33", got.Line)
	}
	if got.LineCorrectedFrom != 30 {
		t.Fatalf("LineCorrectedFrom = %d want 30", got.LineCorrectedFrom)
	}
	if got.Handle != "grep#1" {
		t.Fatalf("handle = %q want grep#1", got.Handle)
	}
}

func TestResolveDoesNotReanchorAmbiguousExcerpt(t *testing.T) {
	readJSON := fmt.Sprintf(`{"path":"dup.go","content":%q,"offset":1,"end_line":40,"limit":40}`, hostmarker.FormatNumberedLines([]string{
		"first occurrence of duplicated excerpt",
		"something else",
		"first occurrence of duplicated excerpt",
	}, 20))
	ev := ledgertest.BuildFromMessages("", []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "read", ID: "c1", Args: map[string]any{"path": "dup.go", "offset": 1, "limit": 40}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted, Content: readJSON}},
	})

	// Cited line 1 (outside window of both line 20 and line 22)
	got := evidence.Resolve(evidence.CitationRoots{}, evidence.Triple{
		Path: "dup.go", Line: 1, Excerpt: "first occurrence of duplicated excerpt",
	}, ev, "")
	if got.LineCorrectedFrom != 0 {
		t.Fatalf("ambiguous excerpt must not reanchor; got LineCorrectedFrom = %d", got.LineCorrectedFrom)
	}
}

func TestResolveReanchorPrecedesOtherPathGuard(t *testing.T) {
	ev := ledgertest.BuildFromMessages("", []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "read", ID: "c1", Args: map[string]any{"path": "a.go", "offset": 30, "limit": 5}},
			{Name: "read", ID: "c2", Args: map[string]any{"path": "b.go", "offset": 50, "limit": 5}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{
			Outcome: api.ToolResultOutcomeCompleted,
			Content: fmt.Sprintf(`{"path":"a.go","content":%q,"offset":30,"end_line":35}`, hostmarker.FormatNumberedLines([]string{"line 30", "line 31", "line 32", "shared verbatim needle"}, 30)),
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{
			Outcome: api.ToolResultOutcomeCompleted,
			Content: fmt.Sprintf(`{"path":"b.go","content":%q,"offset":50,"end_line":55}`, hostmarker.FormatNumberedLines([]string{"shared verbatim needle"}, 50)),
		}},
	})

	// Cited path a.go with line 10 (true line in a.go is 33, while b.go has it at 50)
	got := evidence.Resolve(evidence.CitationRoots{}, evidence.Triple{
		Path: "a.go", Line: 10, Excerpt: "shared verbatim needle",
	}, ev, "")
	if got.Verdict != evidence.VerdictMatched {
		t.Fatalf("verdict = %q want matched (reanchor should precede other path guard)", got.Verdict)
	}
	if got.Line != 33 {
		t.Fatalf("line = %d want 33", got.Line)
	}
	if got.LineCorrectedFrom != 10 {
		t.Fatalf("LineCorrectedFrom = %d want 10", got.LineCorrectedFrom)
	}
}

