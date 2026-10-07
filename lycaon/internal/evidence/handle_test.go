package evidence_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/guidance/ledgertest"
	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestResolveHandle_found(t *testing.T) {
	ev := ledgertest.BuildFromMessages("", []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "read", ID: "c1", Args: map[string]any{"path": "a.go"}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted, Content: "package a"}},
	})
	rec, ok := evidence.ResolveHandle(ev, "read#1")
	if !ok || rec.Kind != "read" || rec.Path != "a.go" {
		t.Fatalf("rec = %+v ok=%v", rec, ok)
	}
}

func TestHandleForPath_newestKind(t *testing.T) {
	ev := ledgertest.BuildFromMessages("", []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "read", ID: "c1", Args: map[string]any{"path": "a.go"}},
			{Name: "edit", ID: "c2", Args: map[string]any{"path": "a.go"}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted, Content: "x"}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted, Content: "ok"}},
	})
	handle, ok := evidence.HandleForPath(ev, "a.go", "edit")
	if !ok || handle != "edit#1" {
		t.Fatalf("handle = %q ok=%v", handle, ok)
	}
}

func TestExcerptMatchesHandle_readBody(t *testing.T) {
	readJSON := fmt.Sprintf(`{"path":"f.go","content":%q,"offset":42,"end_line":42,"limit":1}`, hostmarker.FormatNumberedLines([]string{"return nil"}, 42))
	ev := ledgertest.BuildFromMessages("", []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "read", ID: "c1", Args: map[string]any{"path": "f.go", "offset": 42, "limit": 1}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted, Content: readJSON}},
	})
	if !evidence.ExcerptMatchesHandle(ev, "read#1", "f.go", 42, "return nil") {
		t.Fatal("expected excerpt match")
	}
	if evidence.ExcerptMatchesHandle(ev, "read#1", "f.go", 42, "return 1") {
		t.Fatal("expected excerpt mismatch")
	}
}

func TestExcerptMatchesHandle_taggedReadBody(t *testing.T) {
	readJSON := fmt.Sprintf(`{"path":"lycaon/go.mod","content":%q,"offset":1,"end_line":3,"limit":3,"total_lines":258}`, hostmarker.FormatNumberedLines([]string{"module github.com/lycaon/lycaon", "", "go 1.26.4"}, 1))
	tagged := guidance.PrependHandleTag(readJSON, "read#1")
	ev := ledgertest.BuildFromMessages("", []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "read", ID: "c1", Args: map[string]any{"path": "lycaon/go.mod", "offset": 1, "limit": 3}},
		}},
		{Role: api.MessageRoleTool, Content: tagged, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted, Content: tagged}},
	})
	if !evidence.ExcerptMatchesHandle(ev, "read#1", "lycaon/go.mod", 3, "go 1.26.4") {
		t.Fatal("host handle tag must not break read payload parsing for in-range excerpt")
	}
}

func TestExcerptMatchesHandleAcrossWhitespaceReflow(t *testing.T) {
	readJSON := fmt.Sprintf(`{"path":"a.tsx","content":%q,"offset":35,"end_line":37,"limit":3}`, hostmarker.FormatNumberedLines([]string{"    if (el.innerHTML !== html) {", "      el.innerHTML = html;", "    }"}, 35))
	ev := ledgertest.BuildFromMessages("", []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "read", ID: "c1", Args: map[string]any{"path": "a.tsx", "offset": 35, "limit": 3}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted, Content: readJSON}},
	})
	reflowed := "if (el.innerHTML !== html) {\n el.innerHTML = html;\n }"
	if !evidence.ExcerptMatchesHandle(ev, "read#1", "a.tsx", 35, reflowed) {
		t.Fatal("dedented/reflowed excerpt must verify against the captured body")
	}
	if evidence.ExcerptMatchesHandle(ev, "read#1", "a.tsx", 35, "if (el.outerHTML !== html) {") {
		t.Fatal("a changed identifier must not pass whitespace-tolerant matching")
	}
}

func TestExcerptMatchesHandle_wcCountBody(t *testing.T) {
	// Count metadata is captured as file-region evidence.
	wcJSON := `{"bytes":23526,"lines":669,"path":"lycaon/internal/coordinator/assembly/engine.go"}`
	ev := ledgertest.BuildFromMessages("", []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "wc", ID: "c1", Args: map[string]any{"path": "lycaon/internal/coordinator/assembly/engine.go"}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted, Content: wcJSON}},
	})
	if !evidence.ExcerptMatchesHandle(ev, "wc#1", "lycaon/internal/coordinator/assembly/engine.go", 0, `"bytes":23526,"lines":669`) {
		t.Fatal("a citation of the captured wc count must verify")
	}
	if evidence.ExcerptMatchesHandle(ev, "wc#1", "lycaon/internal/coordinator/assembly/engine.go", 0, `"bytes":23526,"lines":999`) {
		t.Fatal("a fabricated count must not verify")
	}
}

func TestResolveHandleToken_rewritesToPath(t *testing.T) {
	ev := ledgertest.BuildFromMessages("", []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "read", ID: "c1", Args: map[string]any{"path": "lycaon/go.mod"}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted, Content: "module x"}},
	})
	cases := map[string]string{
		`read#1`:               "lycaon/go.mod",
		`read#1:3 ("go 1.26")`: `lycaon/go.mod:3 ("go 1.26")`,
		`read#9`:               "read#9",          // unknown handle stays as-is
		`https://x.test`:       "https://x.test",  // non-handle token untouched
		`lycaon/go.mod:3`:      "lycaon/go.mod:3", // already a path
	}
	for token, want := range cases {
		if got := evidence.ResolveHandleToken(ev, token); got != want {
			t.Fatalf("ResolveHandleToken(%q) = %q want %q", token, got, want)
		}
	}
}

func TestResolveHandleToken_rewritesUnderscoredKind(t *testing.T) {
	ev := evidence.AssembleLedger([]evidence.Record{{
		Handle: "page_geometry#1",
		Kind:   "page_geometry",
		Path:   "artifacts/page.json",
	}})
	got := evidence.ResolveHandleToken(ev, `page_geometry#1:3 ("width": 120)`)
	if got != `artifacts/page.json:3 ("width": 120)` {
		t.Fatalf("ResolveHandleToken = %q", got)
	}
}

func TestNamespaceLedger_prefixesHandles(t *testing.T) {
	child := ledgertest.BuildFromMessages("", []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "grep", ID: "c1", Args: map[string]any{"path": ".", "pattern": "main"}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{
			Outcome: api.ToolResultOutcomeCompleted,
			Content: `{"matches":[{"path":"src/a.go","line":1,"content":"package a"}]}`,
		}},
	})
	namespaced := evidence.NamespaceLedger(child, "child-1")
	rec, ok := evidence.ResolveHandle(namespaced, "child-1:grep#1")
	if !ok || !evidence.PathObserved(namespaced, "src/a.go") {
		t.Fatalf("rec = %+v ok=%v paths=%v", rec, ok, evidence.ObservedPathsSorted(namespaced))
	}
}

func TestNamespaceLedger_composesScopesWithoutChangingHandleIdentity(t *testing.T) {
	rec := evidence.Record{Handle: "read#7", Kind: "read", Path: "src/a.go"}
	once := evidence.NamespaceLedger(evidence.AssembleLedger([]evidence.Record{rec}), "child")
	twice := evidence.NamespaceLedger(once, "delegation")

	handle := "delegation:child:read#7"
	if _, ok := evidence.ResolveHandle(twice, handle); !ok {
		t.Fatalf("missing %q in %v", handle, evidence.HandlesSorted(twice))
	}
	kind, ordinal := evidence.ParseHandleOrdinal(handle)
	if kind != "read" || ordinal != 7 {
		t.Fatalf("ParseHandleOrdinal(%q) = %q, %d", handle, kind, ordinal)
	}
}

func TestNamespaceLedger_preservesDistinctMarkerHandles(t *testing.T) {
	a, ok := evidence.DialedHostRecord("a.example")
	if !ok {
		t.Fatal("expected first dialed-host record")
	}
	b, ok := evidence.DialedHostRecord("b.example")
	if !ok {
		t.Fatal("expected second dialed-host record")
	}

	namespaced := evidence.NamespaceLedger(evidence.AssembleLedger([]evidence.Record{a, b}), "child-1")
	for _, handle := range []string{
		"child-1:dialed_host#a.example",
		"child-1:dialed_host#b.example",
	} {
		if _, ok := evidence.ResolveHandle(namespaced, handle); !ok {
			t.Fatalf("missing %q in %v", handle, evidence.HandlesSorted(namespaced))
		}
	}
	if len(namespaced.Handles) != 2 {
		t.Fatalf("namespaced handles = %v", evidence.HandlesSorted(namespaced))
	}
}

func TestBuildLedgerFromTranscript_handleStability(t *testing.T) {
	msgs := []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "read", ID: "c1", Args: map[string]any{"path": "a.go"}},
			{Name: "grep", ID: "c2", Args: map[string]any{"path": ".", "pattern": "x"}},
			{Name: "edit", ID: "c3", Args: map[string]any{"path": "b.go"}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted, Content: "a"}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted, Content: `{}`}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted, Content: "ok"}},
	}
	ev := ledgertest.BuildFromMessages("", msgs)
	want := []string{"read#1", "grep#1", "edit#1"}
	for _, handle := range want {
		if _, ok := evidence.ResolveHandle(ev, handle); !ok {
			t.Fatalf("missing handle %q in %v", handle, evidence.HandlesSorted(ev))
		}
	}
}

func TestBuildLedgerFromTranscript_failedToolSkipsHandleOrdinal(t *testing.T) {
	msgs := []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "read", ID: "c1", Args: map[string]any{"path": "missing.go"}},
			{Name: "read", ID: "c2", Args: map[string]any{"path": "ok.go"}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{
			Outcome: api.ToolResultOutcomeRejected,
			Content: "Rejected: READ_NOT_FOUND\nCode: READ_NOT_FOUND",
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted, Content: "ok"}},
	}
	ev := ledgertest.BuildFromMessages("", msgs)
	if _, ok := evidence.ResolveHandle(ev, "read#2"); ok {
		t.Fatal("rejected read must not consume ordinal")
	}
	rec, ok := evidence.ResolveHandle(ev, "read#1")
	if !ok || rec.Path != "ok.go" {
		t.Fatalf("read#1 = %+v ok=%v", rec, ok)
	}
}

func TestUnionLegEvidence_namespacedMerge(t *testing.T) {
	childA := []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "read", ID: "c1", Args: map[string]any{"path": "a.go"}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted, Content: "a"}},
	}
	childB := []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "read", ID: "c1", Args: map[string]any{"path": "b.go"}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted, Content: "b"}},
	}
	reader := ledgertest.ChildMessagesReader("", func(id string) []api.Message {
		switch id {
		case "leg-a":
			return childA
		case "leg-b":
			return childB
		default:
			return nil
		}
	})
	ev, err := guidance.UnionLegEvidence(context.Background(), reader, []guidance.EvidenceLeg{{ChildSessionID: "leg-a", LegID: "leg-a"}, {ChildSessionID: "leg-b", LegID: "leg-b"}})
	testutil.FailErr(t, "guidance.UnionLegEvidence failed", err)
	if _, ok := evidence.ResolveHandle(ev.Ledger, "leg-a:read#1"); !ok {
		t.Fatal("missing leg-a handle")
	}
	if _, ok := evidence.ResolveHandle(ev.Ledger, "leg-b:read#1"); !ok {
		t.Fatal("missing leg-b handle")
	}
	if !evidence.PathObserved(ev.Ledger, "a.go") || !evidence.PathObserved(ev.Ledger, "b.go") {
		t.Fatal("expected both paths observed")
	}
}

func TestBuildLedgerFromTranscript_goldenFingerprint(t *testing.T) {
	grepJSON := `{"matches":[{"path":"pkg/main.go","line":1,"content":"package main"}],"receipt":{"tool":"grep"}}`
	msgs := []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "read", ID: "c1", Args: map[string]any{"path": "pkg/main.go"}},
			{Name: "grep", ID: "c2", Args: map[string]any{"path": ".", "pattern": "main"}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted, Content: "package main\n"}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted, Content: grepJSON}},
	}
	ev1 := ledgertest.BuildFromMessages("", msgs)
	ev2 := ledgertest.BuildFromMessages("", msgs)
	if strings.Join(evidence.HandlesSorted(ev1), ",") != strings.Join(evidence.HandlesSorted(ev2), ",") {
		t.Fatalf("handles not stable: %v vs %v", evidence.HandlesSorted(ev1), evidence.HandlesSorted(ev2))
	}
}
