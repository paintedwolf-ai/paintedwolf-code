package sourcefeed

import (
	"context"
	"testing"
)

// A host that is shutting down cancels its callers; a late bind would leave a
// process-wide observer referencing the closed host.
func TestEnsureProjectWatchRefusesCanceledCaller(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	const projectID = "project-canceled-bind"
	t.Cleanup(func() { StopProjectWatch(context.Background(), projectID) })

	bound := EnsureProjectWatch(ctx, projectID, "", []RootSpec{{ID: "r1", WorkspaceID: "ws", Path: t.TempDir()}}, nil)

	if bound {
		t.Fatal("a canceled caller bound a project watch")
	}
	watchRegMu.Lock()
	_, exists := watchers[watchKey{projectID: projectID}]
	watchRegMu.Unlock()
	if exists {
		t.Fatal("a canceled caller registered a project watch")
	}
}
