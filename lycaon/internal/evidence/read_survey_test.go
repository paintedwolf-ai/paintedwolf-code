package evidence_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestReadEvidenceSurvey_expressedScopeNonSurvey(t *testing.T) {
	args := map[string]any{"path": "pkg/a.go", "offset": 1, "limit": 2}
	content := `{"path":"pkg/a.go","mode":"content","content":"1| package main\n","offset":1,"limit":2,"total_lines":10,"truncated":true}`
	if evidence.ReadEvidenceSurvey(args, content) {
		t.Fatal("expressed-scope read should not be survey")
	}
}

func TestReadEvidenceSurvey_unboundedOutlineSurvey(t *testing.T) {
	args := map[string]any{"path": "pkg/a.go"}
	content := `{"path":"pkg/a.go","mode":"outline","symbols":[{"kind":"func","name":"main","line":1}],"total_lines":500,"truncated":false,"selected":0,"total":1}`
	if !evidence.ReadEvidenceSurvey(args, content) {
		t.Fatal("unbounded outline read should be survey")
	}
}

func TestReadEvidenceSurvey_escalatedLiteralNonSurvey(t *testing.T) {
	args := map[string]any{"path": "pkg/a.go"}
	content := `{"path":"pkg/a.go","mode":"content","content":"1| package main\n","offset":1,"limit":500,"total_lines":500,"truncated":false}`
	if evidence.ReadEvidenceSurvey(args, content) {
		t.Fatal("escalated literal full read should not be survey")
	}
}

func TestReadHighlightRecords_mintsFromHighlights(t *testing.T) {
	content := `{"mode":"outline","highlights":[{"path":"src/a.go","line":10,"content":"10| func entry() {}"}]}`
	recs := evidence.ReadHighlightRecords("", content)
	if len(recs) != 1 {
		t.Fatalf("records = %d want 1", len(recs))
	}
	if recs[0].Survey {
		t.Fatal("highlight records must not be survey-grade")
	}
	if recs[0].LineRanges[0].Start != 10 {
		t.Fatalf("line = %d want 10", recs[0].LineRanges[0].Start)
	}
}

func TestPatchReadHighlightHandles_patchesJSON(t *testing.T) {
	in := `{"highlights":[{"path":"a.go","line":1,"content":"x"}]}`
	out, err := evidence.PatchReadHighlightHandles(in, []string{"read#2"})
	testutil.FailErr(t, "evidence.PatchReadHighlightHandles failed", err)
	if out == in {
		t.Fatal("expected patched handles")
	}
	if !strings.Contains(out, `"handle":"read#2"`) {
		t.Fatalf("out = %q", out)
	}
}

func TestSummarizeAnchorRecordsPerAnchor(t *testing.T) {
	content := `{"task":"t","brief":["b"],"anchors":[
		{"path":"src/a.go","line":10,"excerpt":"func entry() {}"},
		{"path":"src/b.go","line":20,"excerpt":"package b"},
		{"path":"src/c.go","line":30,"excerpt":"import x"}
	]}`
	recs := evidence.SummarizeAnchorHighlightRecords("", content)
	if len(recs) != 3 {
		t.Fatalf("records = %d want 3", len(recs))
	}
	if recs[0].Kind != "summarize" || recs[0].SourceTool != "summarize" {
		t.Fatalf("rec[0] = %+v", recs[0])
	}
	if recs[0].LineRanges[0].Start != 10 || recs[0].Body[0] != "func entry() {}" {
		t.Fatalf("rec[0] = %+v", recs[0])
	}
	if recs[2].Path != "src/c.go" || recs[2].Body[0] != "import x" {
		t.Fatalf("rec[2] = %+v", recs[2])
	}
}

func TestSummarizeAnchorRecords_skipsInvalid(t *testing.T) {
	content := `{"anchors":[
		{"path":"","line":1,"excerpt":"x"},
		{"path":"a.go","line":0,"excerpt":"x"},
		{"path":"a.go","line":1,"excerpt":""},
		{"path":"ok.go","line":2,"excerpt":"valid"}
	]}`
	recs := evidence.SummarizeAnchorHighlightRecords("", content)
	if len(recs) != 1 || recs[0].Path != "ok.go" {
		t.Fatalf("records = %+v want one valid anchor", recs)
	}
}

func TestSummarizeNoAnchorsFallsBackToPack(t *testing.T) {
	content := `{"task":"Overview","pack":{"identity":[{"path":"pkg","kind":"dir_map"}]}}`
	recs := evidence.SummarizeAnchorHighlightRecords("", content)
	if len(recs) != 1 || recs[0].Body[0] != "Overview" {
		t.Fatalf("records = %+v want pack task highlight", recs)
	}
}

func TestPatchSummarizeAnchorHandles_patchesJSON(t *testing.T) {
	in := `{"anchors":[{"path":"a.go","line":1,"excerpt":"x"},{"path":"b.go","line":2,"excerpt":"y"}]}`
	out, err := evidence.PatchSummarizeAnchorHandles(in, []string{"summarize#1", "summarize#2"})
	testutil.FailErr(t, "evidence.PatchSummarizeAnchorHandles failed", err)
	if out == in {
		t.Fatal("expected patched handles")
	}
	if !strings.Contains(out, `"handle":"summarize#1"`) || !strings.Contains(out, `"handle":"summarize#2"`) {
		t.Fatalf("out = %q", out)
	}
}

func TestPatchSummarizeAnchorHandles_preservesHostBanner(t *testing.T) {
	banner := "\n>>> Tool feedback\nIdentical summarize call repeated 3 times\nCode: DOOM_LOOP_REPEAT_WARN"
	in := `{"anchors":[{"path":"a.go","line":1,"excerpt":"x"}],"brief":["ok"]}` + banner
	out, err := evidence.PatchSummarizeAnchorHandles(in, []string{"summarize#1"})
	testutil.FailErr(t, "evidence.PatchSummarizeAnchorHandles failed", err)
	if !strings.Contains(out, `"handle":"summarize#1"`) {
		t.Fatalf("missing handle: %q", out)
	}
	if !strings.Contains(out, "Code: DOOM_LOOP_REPEAT_WARN") || !strings.Contains(out, ">>> Tool feedback") {
		t.Fatalf("host banner must survive evidence patch: %q", out)
	}
}

func TestPatchReadHighlightHandles_preservesHostBanner(t *testing.T) {
	banner := "\n>>> Tool feedback\nCode: DOOM_LOOP_REPEAT_WARN"
	in := `{"mode":"outline","highlights":[{"path":"src/a.go","line":10,"content":"10| func entry() {}"}]}` + banner
	out, err := evidence.PatchReadHighlightHandles(in, []string{"read#1"})
	testutil.FailErr(t, "evidence.PatchReadHighlightHandles failed", err)
	if !strings.Contains(out, `"handle":"read#1"`) {
		t.Fatalf("missing handle: %q", out)
	}
	if !strings.Contains(out, "Code: DOOM_LOOP_REPEAT_WARN") {
		t.Fatalf("host banner must survive evidence patch: %q", out)
	}
}

func TestSummarizeAnchorHighlightRecords_fromSpillBanner(t *testing.T) {
	in := "[compacted tool_result — original ~100 tokens; map + verbatim head/tail below are the working set]\n\nverbatim head/tail:\n" +
		`{"task":"t","anchors":[{"path":"a.go","line":1,"excerpt":"package a"}],"brief":["ok"]}`
	recs := evidence.SummarizeAnchorHighlightRecords("", in)
	if len(recs) != 1 {
		t.Fatalf("records = %d want 1", len(recs))
	}
	if recs[0].Path != "a.go" {
		t.Fatalf("path = %q", recs[0].Path)
	}
}

func TestSummarizeAnchorHighlightRecordsRequiresDeclaredEnvelope(t *testing.T) {
	payload := `{"task":"t","anchors":[{"path":"a.go","line":1,"excerpt":"package a"}],"brief":["ok"]}`
	for _, input := range []string{
		"Source text " + payload,
		"verbatim head/tail:\n" + payload,
		"[compacted tool_result]\nverbatim head/tail:\nSource text " + payload,
	} {
		if records := evidence.SummarizeAnchorHighlightRecords("", input); len(records) != 0 {
			t.Errorf("ordinary output minted %d anchors: %q", len(records), input)
		}
	}
	for _, prefix := range []string{
		"[root:child:summarize#1]\n",
		"[compacted tool_result]\nverbatim head/tail:\n[root:child:summarize#1]\n",
	} {
		if records := evidence.SummarizeAnchorHighlightRecords("", prefix+payload); len(records) != 1 {
			t.Errorf("declared envelope minted %d anchors: %q", len(records), prefix)
		}
	}
}
