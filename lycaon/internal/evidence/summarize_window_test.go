package evidence

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSummarizeWindowHandleRetainsFullSourceSpan(t *testing.T) {
	content := `{"anchors":[{"path":"source.go","line":12,"excerpt":"func Route() {"}],"pack":{"substance":[{"path":"source.go","start_line":12,"end_line":14,"body":"12: func Route() {\n13: serve()\n14: }\n"}]}}`
	records := SummarizeAnchorHighlightRecords("/project", content)
	if len(records) != 1 || records[0].LineRanges[0].End != 14 || !strings.Contains(strings.Join(records[0].Body, "\n"), "serve()") {
		t.Fatalf("window evidence=%+v", records)
	}
	patched, err := PatchSummarizeAnchorHandles(content, []string{"summarize#1"})
	testutil.FailErr(t, "patch source window handle", err)
	if strings.Count(patched, `"handle":"summarize#1"`) != 2 {
		t.Fatalf("window handle not propagated: %s", patched)
	}
}

func TestSummarizeHandlesPreserveSourceEncodingBudget(t *testing.T) {
	content := `{"anchors":[{"path":"source.cpp","line":1,"excerpt":"a < b && b > c"}],"pack":{"substance":[{"path":"source.cpp","start_line":1,"end_line":1,"body":"1: a < b && b > c\n"}]}}`
	patched, err := PatchSummarizeAnchorHandles(content, []string{"summarize#1"})
	testutil.FailErr(t, "patch source encoding", err)
	if !strings.Contains(patched, "a < b && b > c") || len(patched)-len(content) > 96 {
		t.Fatalf("evidence encoding inflated the source: %s", patched)
	}
}
