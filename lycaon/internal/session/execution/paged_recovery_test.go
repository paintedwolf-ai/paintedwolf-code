package execution

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/transcript"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestRecoverOrphanedTurnsDrainsMultiplePages(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemory()
	recovery := NewRecovery(mem, transcript.New(mem, nil), NewStatus(mem), nil)
	for i := 0; i < sessionRecoveryPageSize+7; i++ {
		sess, err := mem.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
		testutil.FailErr(t, "Create", err)
		testutil.FailErr(t, "set busy", mem.SetSessionStatus(ctx, sess.ID, api.SessionStatusBusy))
	}

	testutil.FailErr(t, "RecoverOrphanedTurns", recovery.RecoverOrphanedTurns(ctx))
	remaining, err := mem.ListBusySessionIDs(ctx, 1)
	testutil.FailErr(t, "ListBusySessionIDs", err)
	if len(remaining) != 0 {
		t.Fatalf("busy sessions remain after paged recovery: %v", remaining)
	}
}
