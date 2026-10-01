package session_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"

	"github.com/lycaon/lycaon/internal/session"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type stubCoordinatorContext struct {
	ctx wire.CoordinatorRunContext
}

func (s stubCoordinatorContext) BuildCoordinatorTurnFrame(_ context.Context, _ string, _ *wire.Session) (inject.CoordinatorTurnFrame, error) {
	return inject.CoordinatorTurnFrame{RunContext: s.ctx}, nil
}

func TestCoordinatorRunContextDelegatesToBuilder(t *testing.T) {
	store := store.NewMemory()
	mgr := session.NewManager(store, nil, nil, settings.DefaultSessionLimits())
	mgr.SetCoordinatorTurnFrameSource(stubCoordinatorContext{
		ctx: wire.CoordinatorRunContext{WorkflowID: "hotfix-session", CoordinatorBrief: "brief text"},
	})
	ctx := context.Background()
	sess, err := store.Create(ctx, wire.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	runCtx, err := mgr.CoordinatorRunContext(ctx, sess.ID)
	testutil.FailErr(t, "mgr.CoordinatorRunContext failed", err)
	if runCtx.WorkflowID != "hotfix-session" || runCtx.CoordinatorBrief != "brief text" {
		t.Fatalf("run context = %+v", runCtx)
	}
}
