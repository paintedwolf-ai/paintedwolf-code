package survey

import (
	"testing"

	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestLayoutDepthBoundsTheMetadataWalk(t *testing.T) {
	snapshot := sourcecatalog.NewSnapshot([]sourcecatalog.Root{{ID: "root", Path: "/repo"}}, []sourcecatalog.Entry{
		{RootID: "root", Path: "pkg", Name: "pkg", IsDir: true},
		{RootID: "root", Path: "pkg/a.go", Parent: "pkg", Name: "a.go", Size: 1},
		{RootID: "root", Path: "pkg/deep", Parent: "pkg", Name: "deep", IsDir: true},
		{RootID: "root", Path: "pkg/deep/b.go", Parent: "pkg/deep", Name: "b.go", Size: 2},
	})
	for _, tc := range []struct {
		depth, examined int64
	}{
		{1, 1}, {2, 3}, {3, 4},
	} {
		_, examined, coverage, err := layoutRecordsFromSnapshot(t.Context(), "layout", snapshot, "root", ".", ".", nil, 64, &tc.depth)
		testutil.FailErr(t, "bounded layout metadata", err)
		if int64(examined) != tc.examined || int64(coverage.EntriesExamined) != tc.examined {
			t.Fatalf("depth %d: examined %d, coverage %+v", tc.depth, examined, coverage)
		}
	}
	depth := int64(2)
	_, examined, _, err := layoutRecordsFromSnapshot(t.Context(), "layout", snapshot, "root", ".", ".", func(path string, _ bool) bool { return path != "pkg" }, 64, &depth)
	testutil.FailErr(t, "filtered layout metadata", err)
	if examined != 0 {
		t.Fatalf("depth bound bypassed the existing read filter: examined %d", examined)
	}
}

func TestRepoRequestRejectsNegativeDepthBeforeAnyProbe(t *testing.T) {
	depth := int64(-1)
	_, err := (&RepoTool{}).Survey(t.Context(), RepoRequest{Bundle: "layout_overview", LayoutDepth: &depth}, tools.ToolContext{})
	reject := toolrejection.AsToolReject(err)
	if reject == nil || reject.Code != "COMMAND_OPERAND_INVALID" || reject.Data["minimum"] != int64(0) {
		t.Fatalf("negative depth refusal = %+v (%v)", reject, err)
	}
}
