package contractfixture

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	hostapi "github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/sourcefeed"
	"github.com/lycaon/lycaon/internal/testutil"
)

func DrainBackground(t *testing.T, srv *hostapi.Server) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv.WaitForBackground(ctx)
	testutil.FailErr(t, "drain server background work", ctx.Err())
}

// testUserNotices loads the bundled user-notice catalog.

func ReleaseProjectSources(t *testing.T, srv *hostapi.Server) {
	t.Helper()
	if srv.Sources.Workspace.ProjectRegistry == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	projects, err := srv.Sources.Workspace.ProjectRegistry.List(ctx)
	testutil.FailErr(t, "list projects for source release", err)
	for i := range projects {
		sourcefeed.StopProjectWatch(ctx, projects[i].ID)
	}
	// Events delivered before the watches stopped may have scheduled work.
	DrainBackground(t, srv)
	for i := range projects {
		for _, root := range projects[i].Roots {
			path := strings.TrimSpace(root.Path)
			if path == "" {
				continue
			}
			testutil.FailErr(t, "release catalog root "+path, sourcecatalog.Process().Trees.ReleaseTreeRoot(ctx, path))
		}
	}
}

var SourcesReleased sync.Map

// releaseProjectSources releases the process-wide source watches and catalog
// trees as project deletion does; left bound, they write under removed TempDirs
// and keep retrying inventory for the rest of the test binary.

func StopBackgroundOnCleanup(t *testing.T, srv *hostapi.Server) {
	t.Helper()
	t.Cleanup(func() {
		srv.StopBackground()
		DrainBackground(t, srv)
		if _, released := SourcesReleased.LoadOrStore(srv, struct{}{}); !released {
			ReleaseProjectSources(t, srv)
		}
	})
}

// sourcesReleased lets only a server's first teardown release its sources,
// while every registry it was given is still open.
