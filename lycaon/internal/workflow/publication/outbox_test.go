package publication

import (
	"github.com/lycaon/lycaon/internal/eventoutbox"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowpersistence "github.com/lycaon/lycaon/internal/workflow/persistence"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
)

func TestManagerDoesNotDuplicateOutboxedWorkflowMutation(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")
	hub := events.NewMemoryHub()
	ch, unsubscribe, err := hub.Subscribe(t.Context(), events.Subscription{Project: "", Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe", err)
	t.Cleanup(unsubscribe)
	store := workflowpersistence.New(sqlDB)
	store.Transactions.SetEventOutbox(eventoutbox.New(sqlDB, hub))
	publisher := &Runs{Transactions: store.Transactions, Events: &events.Publisher{Hub: hub}}
	publisher.PublishSession(t.Context(), &api.WorkflowRun{
		ID: "run-1", SessionID: "session-1", ProjectID: testdbseed.DefaultProjectID,
		WorkflowID: "plan", Status: api.WorkflowRunStatusRunning, Revision: 1,
	})
	select {
	case event := <-ch:
		t.Fatalf("manager emitted second workflow event outside mutation transaction: %+v", event)
	default:
	}
}
