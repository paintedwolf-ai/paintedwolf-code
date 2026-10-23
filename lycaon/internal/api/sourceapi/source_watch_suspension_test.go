package sourceapi

import (
	"testing"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/sourcefeed"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSuspendedAndDeletedProjectsCannotRebindCopiedWatch(t *testing.T) {
	server := newSourceHandlerFixture(t, func(d *Deps) { d.WatchNeedsSeed = func(string) bool { return false } })
	p, err := project.CreateWithRoot(t.Context(), server.Watch.ProjectRegistry, t.TempDir())
	testutil.FailErr(t, "create watched project", err)
	t.Cleanup(func() { _ = sourcefeed.StopProjectWatch(t.Context(), p.ID) })
	if !server.Watch.ensureWorkspaceWatch(t.Context(), p) {
		t.Fatal("initial binding failed")
	}
	resume, err := server.Watch.SuspendProjectWatch(t.Context(), p.ID)
	testutil.FailErr(t, "suspend project binding", err)
	if server.Watch.ensureWorkspaceWatch(t.Context(), p) || repochange.Coverage(p.Roots[0].Path).Watching {
		t.Fatal("suspended project rebound")
	}
	resume()
	resume()
	if !server.Watch.ensureWorkspaceWatch(t.Context(), p) {
		t.Fatal("resumed project did not bind")
	}
	resume, err = server.Watch.SuspendProjectWatch(t.Context(), p.ID)
	testutil.FailErr(t, "suspend before deletion", err)
	testutil.FailErr(t, "delete project", server.Watch.ProjectRegistry.Delete(t.Context(), p.ID))
	resume()
	if server.Watch.ensureWorkspaceWatch(t.Context(), p) {
		t.Fatal("deleted project rebound from copied state")
	}
}
