package events

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestProjectRegistryLifecycleReachesOtherProjects(t *testing.T) {
	hub := NewMemoryHub()
	boundary := hub.CurrentCursor()
	live, stop, err := hub.Subscribe(t.Context(), Subscription{Project: "2a730f9c-2186-5a1a-b1e8-0b76afd4231b", Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe foreground", err)
	defer stop()
	for _, action := range []api.ProjectEventAction{
		api.ProjectEventCreated, api.ProjectEventUpdated, api.ProjectEventDeleted,
	} {
		testutil.FailErr(t, "publish registry mutation", hub.Publish(t.Context(), api.EventTopicProject,
			PublishKey{Project: "0d6a6bf3-f88d-5b05-bdf0-c91fbd057d57"}, api.ProjectEvent{ID: "0d6a6bf3-f88d-5b05-bdf0-c91fbd057d57", Action: action}))
	}
	replay, stopReplay, err := hub.Subscribe(t.Context(), Subscription{Project: "2a730f9c-2186-5a1a-b1e8-0b76afd4231b", Viewer: testutil.HostOwner(), After: boundary})
	testutil.FailErr(t, "replay registry mutations", err)
	defer stopReplay()
	for range 3 {
		current := testutil.Receive(t, "live registry mutation", live)
		retained := testutil.Receive(t, "replayed registry mutation", replay)
		if current.EventID != retained.EventID || current.Scope.ProjectID != "0d6a6bf3-f88d-5b05-bdf0-c91fbd057d57" {
			t.Fatalf("registry delivery lost identity or subject: live=%+v replay=%+v", current, retained)
		}
	}
}

func TestEventAudienceLiveReplayParity(t *testing.T) {
	for _, topic := range api.AllEventTopicValues() {
		t.Run(string(topic), func(t *testing.T) {
			for _, subject := range []string{"", "2a730f9c-2186-5a1a-b1e8-0b76afd4231b", "0d6a6bf3-f88d-5b05-bdf0-c91fbd057d57"} {
				t.Run("subject="+subject, func(t *testing.T) {
					key := PublishKey{Project: subject}
					if ValidatePublishScope(topic, key) != nil {
						return
					}
					hub := NewMemoryHub()
					boundary := hub.CurrentCursor()
					live, stop, err := hub.Subscribe(t.Context(), Subscription{Project: "2a730f9c-2186-5a1a-b1e8-0b76afd4231b", Viewer: testutil.HostOwner()})
					testutil.FailErr(t, "subscribe live", err)
					defer stop()
					testutil.FailErr(t, "publish event", hub.Publish(t.Context(), topic, key, struct{}{}))
					hub.FlushDebounced()
					replay, stopReplay, err := hub.Subscribe(t.Context(), Subscription{Project: "2a730f9c-2186-5a1a-b1e8-0b76afd4231b", Viewer: testutil.HostOwner(), After: boundary})
					testutil.FailErr(t, "subscribe replay", err)
					defer stopReplay()
					want := 0
					if topic == api.EventTopicProject || subject == "" || subject == "2a730f9c-2186-5a1a-b1e8-0b76afd4231b" {
						want = 1
					}
					if len(live) != want || len(replay) != want {
						t.Fatalf("audience: live=%d replay=%d want=%d", len(live), len(replay), want)
					}
				})
			}
		})
	}
}
