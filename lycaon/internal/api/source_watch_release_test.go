package api

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testutil"
)

// A closed host leaves no process-wide project watch that calls back into it.
func TestStopSourceWatchesUnbindsTheHostsProjects(t *testing.T) {
	srv := newTestServer(t, func(d *Dependencies) {
		d.WatchNeedsSeed = func(string) bool { return false }
		d.CatalogSnapshot = func(context.Context, string, []sourcecatalog.Root) (sourcecatalog.Snapshot, error) {
			return sourcecatalog.Snapshot{}, nil
		}
	})
	p, err := project.CreateWithRoot(t.Context(), srv.projectRegistry, t.TempDir())
	testutil.FailErr(t, "create project", err)
	srv.Sources.EnsureSourceWatch(t.Context(), p.ID)
	if !repochange.Coverage(p.Roots[0].Path).Watching {
		t.Fatal("project root is not watched after binding")
	}

	srv.Sources.StopSourceWatches(t.Context())

	if coverage := repochange.Coverage(p.Roots[0].Path); coverage.Watching {
		t.Fatalf("project root is still watched after the host stopped its watches: %+v", coverage)
	}
}
