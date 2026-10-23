package sourcefeed

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestProjectWatchStopReportsUndrainedCallbackAndSealsCopies(t *testing.T) {
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	projectID := t.Name()
	EnsureProjectWatch(t.Context(), nil, projectID, "", []RootSpec{{ID: "root", WorkspaceID: "workspace", Path: t.TempDir()}}, func(context.Context, string, ExternalBatch) {
		close(entered)
		<-release
	})
	watchRegMu.Lock()
	watch := watchers[watchKey{projectID, ""}]
	watchRegMu.Unlock()
	go func() { defer close(done); watch.flushExternalChanges(t.Context(), pendingExternalBatch{resync: true}) }()
	<-entered
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := StopProjectWatch(ctx, projectID); !errors.Is(err, context.Canceled) {
		t.Fatalf("stop with active callback = %v", err)
	}
	if err := StopProjectWatch(ctx, projectID); !errors.Is(err, context.Canceled) {
		t.Fatalf("retry lost active callback = %v", err)
	}
	close(release)
	<-done
	testutil.FailErr(t, "drain released callback", StopProjectWatch(t.Context(), projectID))
	// A copied watch cannot invoke the callback again after routing is stopped.
	watch.flushExternalChanges(t.Context(), pendingExternalBatch{resync: true})
}
