package delegation

import (
	"testing"

	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestGroundingVerdictUsesDelegationProjectIdentity(t *testing.T) {
	store := NewMemoryStore()
	d, err := store.Create(t.Context(), api.Delegation{ProjectID: testdbseed.DefaultProjectID, WorkspacePath: t.TempDir(), Task: "Check grounding", Strategy: api.HuntStrategyFileBased}, "session", []api.Leg{{ID: "leg", Title: "Fixture leg", Status: api.LegStatusPending}})
	testutil.FailErr(t, "create delegation", err)
	hub := events.NewMemoryHub()
	ch, unsubscribe, err := hub.Subscribe(t.Context(), events.Subscription{Project: d.ProjectID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe to project events", err)
	defer unsubscribe()
	coordinator := NewGroundingCoordinator(store, nil, nil, DefaultGroundingConfig(), nil, nil)
	coordinator.Events = &events.Publisher{Hub: hub}
	coordinator.publishGroundingVerdict(t.Context(), "session", d.ID, GroundingVerdict{Code: "COORDINATOR_UNGROUNDED_CLAIM", LegID: "leg"})
	select {
	case event := <-ch:
		if event.Scope.ProjectID != d.ProjectID || event.Scope.SessionID != "session" || event.Topic != api.EventTopicGrounding {
			t.Fatalf("grounding scope: %+v", event)
		}
	default:
		t.Fatal("grounding event was not delivered to its project")
	}
}
