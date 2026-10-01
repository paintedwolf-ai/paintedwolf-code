package toolhost

import (
	"testing"

	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSourceContextUsesReturnedGitStatusPage(t *testing.T) {
	ctx := tools.ToolContext{ProjectID: "p", ActiveRootID: "r", Roots: []projectroot.RootRef{{ID: "r", Path: t.TempDir()}}, Out: &tools.ToolInvocationOut{}}
	status := &git.GitStatus{Branch: "main", Files: []git.GitStatusEntry{{Path: "first/a.go"}, {Path: ".ignored/a.go"}, {Path: "third/a.go"}}}
	_, err := gitStatusOutput(status, git.StatusToolPage{Offset: 1, Limit: 1}, nil, ctx)
	testutil.FailErr(t, "return status page", err)
	got := ctx.Out.SourceContext
	if got == nil || got.Truncated || len(got.Locations) != 1 || got.Locations[0].Path != ".ignored/a.go" || got.Locations[0].EntryKind != api.NavigationEntryKindFile {
		t.Fatalf("status page sources = %+v", got)
	}
}
