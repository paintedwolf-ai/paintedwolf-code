package summarize

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/fileoutline"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestBriefContentBuildsBoundedEvidencePack(t *testing.T) {
	caps := DefaultCaps()
	caps.Pack.InputBudgetTokens = 320
	caps.Gather.SymbolWindowLines = 6
	caps.Anchors.Default = 3
	source := "package sample\n\nimport \"fmt\"\n\nfunc Build() error {\n\tfmt.Println(\"build\")\n\treturn nil\n}\n\nfunc helper() {}\n"

	res, err := BriefContent(context.Background(), ContentInput{
		Path: "sample.go", Content: source, Task: "explain the file",
		Analysis: fileoutline.AnalyzeText(context.Background(), "sample.go", []byte(source)),
	}, caps)
	testutil.FailErr(t, "build content briefing", err)
	if len(res.Pack.Identity) != 1 || res.Pack.Identity[0].Path != "sample.go" {
		t.Fatalf("identity = %+v", res.Pack.Identity)
	}
	if len(res.Pack.Skeleton) == 0 || len(res.Anchors) == 0 {
		t.Fatalf("pack lacks structure or anchors: %+v", res)
	}
	if got := strings.Join([]string{res.Anchors[0].Path, res.Anchors[0].Excerpt}, " "); !strings.Contains(got, "sample.go") {
		t.Fatalf("anchor = %+v", res.Anchors[0])
	}
}
