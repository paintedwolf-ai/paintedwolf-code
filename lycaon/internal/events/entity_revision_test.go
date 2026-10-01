package events

import (
	"context"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestEntityRevisionReachesTheWire(t *testing.T) {
	ctx := context.Background()
	hub := NewMemoryHub()
	pub := &Publisher{
		Hub:            hub,
		SessionProject: func(context.Context, string) (string, bool) { return "54f6d2ef-89c3-56e9-9a76-f6b1a2a54476", true },
	}

	ch, unsubscribe, err := hub.Subscribe(ctx, Subscription{Project: "54f6d2ef-89c3-56e9-9a76-f6b1a2a54476", Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "hub.Subscribe", err)
	defer unsubscribe()

	cases := []struct {
		name    string
		topic   api.EventTopic
		publish func()
		want    uint64
	}{
		{
			"workflow run transition carries the run revision",
			api.EventTopicWorkflow,
			func() {
				pub.PublishWorkflow(ctx, "54f6d2ef-89c3-56e9-9a76-f6b1a2a54476", "session-1", api.WorkflowEvent{
					Event: "run.updated", WorkflowRunID: "run-1",
					Run: &api.WorkflowRun{ID: "run-1", SessionID: "session-1", Revision: 7},
				})
			},
			7,
		},
		{"progress carries its session revision", api.EventTopicProgress, func() { pub.PublishProgress(ctx, "session-1", 3) }, 3},
		{"queue carries its draft revision", api.EventTopicQueue, func() { pub.PublishQueue(ctx, "session-1", 4) }, 4},
		{"findings carries its session revision", api.EventTopicFindings, func() { pub.PublishFindings(ctx, "session-1", 5) }, 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.publish()
			hub.FlushDebounced()
			deadline := time.After(2 * time.Second)
			for {
				select {
				case env := <-ch:
					// PublishWorkflow also fans out board and attention refreshes.
					if env.Topic != tc.topic {
						continue
					}
					if env.EntityRevision != tc.want {
						t.Fatalf("%s entity_revision = %d, want %d", env.Topic, env.EntityRevision, tc.want)
					}
					return
				case <-deadline:
					t.Fatal("timeout waiting for the event under test")
				}
			}
		})
	}
}
