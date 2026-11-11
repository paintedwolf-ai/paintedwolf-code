package toolhost

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/summarize"
	"github.com/lycaon/lycaon/internal/survey"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/native/mutation"
	surveytools "github.com/lycaon/lycaon/internal/tools/native/survey"
)

// The fixture uses the production owner composition over the real entry,
// admission, aggregate memory, persistence and supervision from programtest.
func newComposedProgramFixture(t *testing.T) *programtest.Fixture {
	t.Helper()
	f := programtest.New(t, programtest.Options{})
	testutil.FailErr(t, "register diff owner", f.Runtime.Registry.Register("diff", func(context.Context, map[string]any, tools.ToolContext) (string, error) {
		return "", nil
	}))
	for _, form := range formTools {
		tool := &native.FormTool{Programs: f.Runtime, Argv: form.argv}
		testutil.FailErr(t, "register form tool "+form.name, f.Runtime.Registry.Register(form.name, tool.Run))
	}
	b := f.Runtime.Boundary
	catalog := sourcecatalog.Process()
	summarizeCaps, err := summarize.LoadCaps()
	testutil.FailErr(t, "load summarize caps", err)
	summarizeTool := &surveytools.SummarizeTool{Boundary: b, Caps: summarizeCaps, Catalog: catalog}
	surveyRepo := &survey.RepoTool{Boundary: b, Caps: survey.DefaultCaps(), SourceCatalog: catalog}
	owners, err := (programOwnerTools{
		boundary:  b,
		summarize: summarizeTool,
		repo:      surveyRepo,
		catalog:   catalog,
		git:       git.NewManager(),
		registry:  f.Runtime.Registry,
		operations: mutation.Operations{
			Write:   &native.WriteTool{Boundary: b},
			Delete:  &native.DeleteTool{Boundary: b},
			Move:    &native.MoveTool{Boundary: b},
			Copy:    &native.CopyTool{Boundary: b},
			Mkdir:   &native.MkdirTool{Boundary: b},
			Chmod:   &native.ChmodTool{Boundary: b},
			Chown:   &native.ChownTool{Boundary: b},
			Times:   &native.TimesTool{Boundary: b},
			Extract: &native.ExtractArchiveTool{Boundary: b},
		},
	}).compose(f.Recorder)
	testutil.FailErr(t, "compose program owners", err)
	f.Runtime.Owners = owners
	return f
}
