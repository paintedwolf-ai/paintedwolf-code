package search

import (
	"testing"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	catalogtest "github.com/lycaon/lycaon/internal/testsetup/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testutil"
)

// An export cannot vouch for code matches in a root whose catalog has not
// settled, so it reports truncation until discovery finishes.
func TestExportReportsTruncatedWhileSourceRootWarms(t *testing.T) {
	root := sourcecatalog.Root{ID: "export-warming-root", Path: t.TempDir()}
	svc := &Service{router: NewRouter(
		fakeExecutor{source: ExecutorStore},
		&CodeExecutor{catalog: sourcecatalog.Process()},
		fakeExecutor{source: ExecutorSymbol},
	)}
	compileCtx := CompileContext{
		OriginProjectID:    "p1",
		AttachedProjectIDs: func() ([]string, error) { return []string{"p1"}, nil },
		RootsForProject: func(string) ([]CodeRoot, error) {
			return []CodeRoot{{RootID: root.ID, Path: root.Path}}, nil
		},
	}

	release := holdDirectoryAdmission(t)
	warming, err := svc.Export(t.Context(), "small-export-token", compileCtx)
	release()
	testutil.FailErr(t, "export while warming", err)
	if !warming.Truncated || !hasCoverageIssue(warming.Result.Issues, IssueCatalogWarming) {
		t.Fatalf("warming export truncated=%v issues=%+v, want truncated with catalog_warming", warming.Truncated, warming.Result.Issues)
	}

	testutil.FailErr(t, "settle source inventory", catalogtest.AwaitIndex(t.Context(), sourcecatalog.Process(), "p1", root))
	settled, err := svc.Export(t.Context(), "small-export-token", compileCtx)
	testutil.FailErr(t, "export after settle", err)
	if settled.Truncated {
		t.Fatalf("settled export truncated, issues=%+v", settled.Result.Issues)
	}
}

// holdDirectoryAdmission occupies every directory-read slot on the process
// broker, so no cold root can finish discovery until the release runs.
func holdDirectoryAdmission(t *testing.T) func() {
	t.Helper()
	broker := backgroundwork.Process()
	var releases []func()
	for _, lane := range []string{"export-hold-a", "export-hold-b"} {
		release, err := broker.Acquire(t.Context(), backgroundwork.Request{
			Priority:  backgroundwork.PriorityInteractive,
			Lane:      lane,
			Resources: []backgroundwork.Resource{backgroundwork.ResourceDirectory},
			Units:     map[backgroundwork.Resource]int{backgroundwork.ResourceDirectory: 4},
		})
		testutil.FailErr(t, "hold directory admission", err)
		releases = append(releases, release)
	}
	released := false
	releaseAll := func() {
		if released {
			return
		}
		released = true
		for _, release := range releases {
			release()
		}
	}
	t.Cleanup(releaseAll)
	return releaseAll
}
