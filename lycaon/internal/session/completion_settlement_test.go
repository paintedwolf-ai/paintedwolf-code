package session

import (
	"testing"

	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSettlementCannotCompleteWithOpenWorkflowGates(t *testing.T) {
	mgr, sess := newSynthesisDelayManager(t)
	mgr.SetWorkflowDomains(workflowDomainFixture(gatedCloseoutView{state: gatedExecuteState()}))
	hub := events.NewMemoryHub()
	mgr.SetEventPublisher(&events.Publisher{Hub: hub})
	eventsCh, unsubscribe, err := hub.Subscribe(t.Context(), events.Subscription{Project: sess.ProjectID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe to settlement", err)
	t.Cleanup(unsubscribe)
	testutil.FailErr(t, "settle incomplete workflow", mgr.Runner.Settlement.Settle(t.Context(), sess.ID, api.SessionIdleDispositionCompleted))
	if got := awaitIdleDisposition(t, eventsCh); got != api.SessionIdleDispositionTurnError {
		t.Fatalf("disposition=%s", got)
	}
}
