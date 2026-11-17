package session

import (
	"context"
	"errors"
	"testing"

	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestChatDeleteDisposesItsResourceScope(t *testing.T) {
	database := testdbfixture.Open(t, "session.db")
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, t.TempDir())
	st := sessionstore.NewSQL(database)
	mgr := newTestManagerWithStore(t, st)
	var disposed []string
	testutil.FailErr(t, "register cleanup", mgr.Resources.RegisterCleanup("disposal-probe", 99, func(_ context.Context, id string) error {
		disposed = append(disposed, id)
		return nil
	}))
	ctx := context.Background()
	kept, err := st.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create kept chat", err)
	deleted, err := st.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create deleted chat", err)

	testutil.FailErr(t, "delete chat", mgr.Chats.Delete(ctx, deleted.ID))

	if len(disposed) != 1 || disposed[0] != deleted.ID {
		t.Fatalf("disposed scopes = %v, want [%s]", disposed, deleted.ID)
	}
	if _, err := st.Get(ctx, deleted.ID); !errors.Is(err, sessionstore.ErrSessionNotFound) {
		t.Fatalf("deleted chat lookup err = %v, want ErrSessionNotFound", err)
	}
	if _, err := st.Get(ctx, kept.ID); err != nil {
		t.Fatalf("kept chat lookup: %v", err)
	}
}

func TestTerminalWorkerTransitionForgetsItsWakeDigest(t *testing.T) {
	mgr, _ := newTestManager(t)
	mgr.Workers.Digests.Put("job-done", "done digest")
	mgr.Workers.Digests.Put("job-running", "running digest")

	mgr.Coordinator.Workers.Terminal(context.Background(), "parent", "job-done")

	if got := mgr.Workers.Digests.Take("job-done"); got != "" {
		t.Fatalf("terminal job digest = %q, want forgotten", got)
	}
	if got := mgr.Workers.Digests.Take("job-running"); got != "running digest" {
		t.Fatalf("running job digest = %q, want kept", got)
	}
}
