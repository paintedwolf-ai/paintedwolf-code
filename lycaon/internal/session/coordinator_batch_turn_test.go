package session_test

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/batch"
	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCoordinatorBatchTurn_oneSynthesisPerTurnLatch(t *testing.T) {
	hints, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "load hints", err)
	store := store.NewMemory()
	mgr := session.NewManager(store, nil, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	mgr.SetRejectFormatter(guidance.NewStaticRejectFormatter(hints))

	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{ProjectID: "coordinator"}, "coordinator")
	testutil.FailErr(t, "create session", err)

	mgr.BeginPromptTurnForTest(sess.ID, "")
	mgr.AcceptCoordinatorGroundedSynthesisForTest(ctx, sess.ID)

	reject, block := guard.FormatHostNoToolTurnReject(
		sess,
		[]api.Message{{Role: api.MessageRoleUser, Origin: api.MessageOriginHost, Visibility: api.MessageVisibilityInternal, Kind: api.MessageKindHostLoopWake}},
		"Another report in the same turn.",
		nil,
		"implement_synthesis",
		true,
		surface.ImplementSessionState{BatchPhase: batch.PhaseSynthesize},
		mgr.RejectFormatterForTest(),
		mgr.CoordinatorBatchTurnGuardForTest(sess.ID),
	)
	if !block || !strings.Contains(reject, guard.CoordinatorBatchAlreadyClosedCode) {
		t.Fatalf("reject=%q block=%v want latch block before scaffold reads closed", reject, block)
	}
}

func TestCoordinatorBatchTurn_groundingRetryDoesNotSetLatch(t *testing.T) {
	store := store.NewMemory()
	mgr := session.NewManager(store, nil, tools.NewStubRegistry(), settings.DefaultSessionLimits())

	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{ProjectID: "coordinator"}, "coordinator")
	testutil.FailErr(t, "create session", err)

	mgr.BeginPromptTurnForTest(sess.ID, "")
	if mgr.CoordinatorBatchTurnGuardForTest(sess.ID).SynthesisAcceptedThisTurn {
		t.Fatal("fresh turn should not have synthesis latch")
	}
}
