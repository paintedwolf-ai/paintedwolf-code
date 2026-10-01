package events

import (
	"testing"
	"time"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestActivitySnapshotsReplaceMissedEdges(t *testing.T) {
	for _, kind := range []api.ActivityKind{"running_tool", "awaiting_wake", "preparing_context"} {
		t.Run(string(kind), func(t *testing.T) {
			publisher := &Publisher{}
			lease := api.ActivityEvent{ActivityID: "lease", Kind: kind, Status: api.ActivityStatusActive, StartedAt: time.Now(), WaitTriggers: []string{"timer"}}
			publisher.PublishActivity(t.Context(), "project", "session", lease)
			publisher.PublishActivity(t.Context(), "project", "other", lease)
			lease.WaitTriggers[0] = "changed"
			snapshot := publisher.SessionActivities("session")
			if len(snapshot) != 1 || snapshot[0].WaitTriggers[0] != "timer" {
				t.Fatalf("active snapshot lost lease ownership: %+v", snapshot)
			}
			snapshot[0].WaitTriggers[0] = "mutated snapshot"
			if publisher.SessionActivities("session")[0].WaitTriggers[0] != "timer" {
				t.Fatal("snapshot mutation changed the publisher's active lease")
			}
			lease.Status = api.ActivityStatusDone
			publisher.PublishActivity(t.Context(), "project", "session", lease)
			publisher.PublishActivity(t.Context(), "project", "session", lease)
			if got := publisher.SessionActivities("session"); len(got) != 0 {
				t.Fatalf("settled lease survived: %+v", got)
			}
			if got := publisher.SessionActivities("other"); len(got) != 1 {
				t.Fatalf("settlement changed another session: %+v", got)
			}
			if got := (&Publisher{}).SessionActivities("other"); len(got) != 0 {
				t.Fatalf("new host inherited process-local work: %+v", got)
			}
		})
	}
}
