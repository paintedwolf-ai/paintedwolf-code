package evidence_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance/ledgertest"
	"github.com/lycaon/lycaon/pkg/api"
)

func readRecord(path, content string, start, end int) evidence.Record {
	return evidence.Record{
		Handle:     "read#1",
		Kind:       "read",
		Shape:      evidence.ShapeFileRegion,
		Path:       path,
		Body:       []string{content},
		LineRanges: []evidence.LineRange{{Start: start, End: end}},
	}
}

func TestFileRegion_FabricatedLineNumberDoesNotGround(t *testing.T) {
	body := "10\tunrelated line ten\n11\tunrelated line eleven\n4000\t.root { x\n"
	rec := readRecord("dup.go", body, 10, 4000)

	ok, _ := evidence.VerifyRecord(rec, evidence.Claim{Path: "dup.go", Line: 12})
	if !ok {
		t.Fatal("line-without-excerpt should degrade to path grounding")
	}
	ok, _ = evidence.VerifyRecord(rec, evidence.Claim{Path: "dup.go", Line: 12, Excerpt: ".root { x"})
	if ok {
		t.Fatal("excerpt at line 4000 must not ground at cited line 12")
	}
}

func TestFileRegion_WrongLineExcerptFails(t *testing.T) {
	body := "10\talpha\n11\tbeta\n4000\t.root { x\n"
	rec := readRecord("x.go", body, 10, 4000)

	ok, _ := evidence.VerifyRecord(rec, evidence.Claim{Path: "x.go", Line: 12, Excerpt: ".root { x"})
	if ok {
		t.Fatal("expected wrong-line excerpt to fail")
	}
	ok, _ = evidence.VerifyRecord(rec, evidence.Claim{Path: "x.go", Line: 4000, Excerpt: ".root { x"})
	if !ok {
		t.Fatal("expected excerpt at cited line to pass")
	}
}

func TestFileRegion_LineBindWindowDrift(t *testing.T) {
	body := "12\tbefore\n13\t.root { x\n"
	rec := readRecord("w.go", body, 12, 13)

	ok, _ := evidence.VerifyRecord(rec, evidence.Claim{Path: "w.go", Line: 12, Excerpt: ".root { x"})
	if !ok {
		t.Fatal("expected ±window drift to pass")
	}
	ok, _ = evidence.VerifyRecord(rec, evidence.Claim{Path: "w.go", Line: 20, Excerpt: ".root { x"})
	if ok {
		t.Fatal("expected excerpt far from cited line to fail")
	}
}

func TestFileRegion_MultiLineExcerptAnchoredAtLine(t *testing.T) {
	body := "35\t    if (el.innerHTML !== html) {\n36\t      el.innerHTML = html;\n37\t    }\n"
	rec := readRecord("a.tsx", body, 35, 37)

	reflowed := "if (el.innerHTML !== html) {\n el.innerHTML = html;\n }"
	ok, _ := evidence.VerifyRecord(rec, evidence.Claim{Path: "a.tsx", Line: 35, Excerpt: reflowed})
	if !ok {
		t.Fatal("expected contiguous multi-line excerpt at anchor to pass")
	}
	ok, _ = evidence.VerifyRecord(rec, evidence.Claim{Path: "a.tsx", Line: 20, Excerpt: reflowed})
	if ok {
		t.Fatal("expected excerpt far from cited line to fail")
	}
}

func TestFileRegion_GrepRecordExcerptAtLine(t *testing.T) {
	root := t.TempDir()
	grepJSON := `{"matches":[{"path":"src/a.go","line":42,"content":"return nil"}]}`
	ev := ledgertest.BuildFromMessages(root, []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "grep", ID: "c1", Args: map[string]any{"path": ".", "pattern": "nil"}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted, Content: grepJSON}},
	})
	rec, ok := evidence.ResolveHandle(ev, "grep#1")
	if !ok {
		t.Fatal("missing grep record")
	}

	ok, _ = evidence.VerifyRecord(rec, evidence.Claim{Path: "src/a.go", Line: 42, Excerpt: "return nil"})
	if !ok {
		t.Fatal("expected grep line match")
	}
	ok, _ = evidence.VerifyRecord(rec, evidence.Claim{Path: "src/a.go", Line: 42, Excerpt: "return 1"})
	if ok {
		t.Fatal("expected grep line mismatch")
	}
	ok, _ = evidence.VerifyRecord(rec, evidence.Claim{Path: "src/a.go", Line: 99, Excerpt: "return nil"})
	if ok {
		t.Fatal("expected wrong grep line to fail")
	}
}

func TestFileRegion_PathOnlyAndHandleOnlyUnchanged(t *testing.T) {
	rec := readRecord("p.go", "1\tpackage p\n", 1, 1)

	ok, _ := evidence.VerifyRecord(rec, evidence.Claim{Path: "p.go"})
	if !ok {
		t.Fatal("expected path-only grounding")
	}
	ok, _ = evidence.VerifyRecord(rec, evidence.Claim{})
	if !ok {
		t.Fatal("expected handle-only coarse grounding")
	}
}

func TestFileRegion_ExcerptOnlyUnchanged(t *testing.T) {
	rec := readRecord("f.go", "99\tneedle appears here\n", 99, 99)

	ok, _ := evidence.VerifyRecord(rec, evidence.Claim{Excerpt: "needle appears"})
	if !ok {
		t.Fatal("expected excerpt-only anywhere-in-body grounding")
	}
}

func TestFileRegion_TrivialityFloorUnchanged(t *testing.T) {
	readJSON := `{"path":"f.go","content":"1|if x","offset":1,"end_line":1,"limit":1}`
	ev := ledgertest.BuildFromMessages("", []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "read", ID: "c1", Args: map[string]any{"path": "f.go"}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{
			Outcome: api.ToolResultOutcomeCompleted,
			Content: readJSON,
		}},
	})
	if evidence.ExcerptMatchesHandle(ev, "read#1", 1, "if") {
		t.Fatal("sub-min-span excerpt must not verify")
	}
}

func TestExcerptMatchesHandle_readBodyLineBound(t *testing.T) {
	readJSON := `{"path":"f.go","content":"42|  return nil","offset":42,"end_line":42,"limit":1}`
	ev := ledgertest.BuildFromMessages("", []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "read", ID: "c1", Args: map[string]any{"path": "f.go", "offset": 42, "limit": 1}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted, Content: readJSON}},
	})
	if !evidence.ExcerptMatchesHandle(ev, "read#1", 42, "return nil") {
		t.Fatal("expected excerpt match at cited line")
	}
	if evidence.ExcerptMatchesHandle(ev, "read#1", 42, "return 1") {
		t.Fatal("expected excerpt mismatch")
	}
	if evidence.ExcerptMatchesHandle(ev, "read#1", 1, "return nil") {
		t.Fatal("expected wrong cited line to fail")
	}
}

func TestLineWithoutExcerptDegradesViaHandle(t *testing.T) {
	ev := ledgertest.BuildFromMessages("", []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "read", ID: "c1", Args: map[string]any{"path": "a.go"}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{
			Outcome: api.ToolResultOutcomeCompleted,
			Content: `{"path":"a.go","content":"1\tpackage a\n","offset":1,"end_line":1}`,
		}},
	})
	if !evidence.ExcerptMatchesHandle(ev, "read#1", 12, "") {
		t.Fatal("line-without-excerpt should degrade to handle grounding")
	}
}
