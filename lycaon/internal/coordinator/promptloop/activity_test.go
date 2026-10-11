package promptloop

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestBeginActivityPublishesMatchingLifecycleEdges(t *testing.T) {
	projectID := eventFixtureProject(t)
	hub := events.NewMemoryHub()
	loop := NewPromptLoop(PromptLoopDeps{
		Projection: ProjectionDeps{
			Events: &events.Publisher{Hub: hub},
		},
	})
	ctx := t.Context()
	stream, unsubscribe, err := hub.Subscribe(ctx, events.Subscription{Project: projectID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe", err)
	defer unsubscribe()

	finish := loop.Projection.beginActivity(ctx, &api.Session{ProjectID: projectID}, "session-1", api.ActivityKindRunningTool, "verify", "call-1")
	finish.finish()

	first := activityEventFromStream(t, stream)
	second := activityEventFromStream(t, stream)
	if first.Status != api.ActivityStatusActive || second.Status != api.ActivityStatusDone {
		t.Fatalf("statuses = %q, %q want active, done", first.Status, second.Status)
	}
	if first.ActivityID == "" || second.ActivityID != first.ActivityID {
		t.Fatalf("activity ids = %q, %q want one non-empty id", first.ActivityID, second.ActivityID)
	}
	if first.SessionID != "session-1" || first.ToolName != "verify" || first.ToolCallID != "call-1" {
		t.Fatalf("active event = %+v", first)
	}
}

func activityEventFromStream(t *testing.T, stream <-chan api.EventEnvelope) api.ActivityEvent {
	t.Helper()
	select {
	case envelope := <-stream:
		if envelope.Topic != api.EventTopicActivity {
			t.Fatalf("topic = %q want activity", envelope.Topic)
		}
		var event api.ActivityEvent
		testutil.FailErr(t, "decode activity", json.Unmarshal(envelope.Data, &event))
		return event
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for activity event")
		return api.ActivityEvent{}
	}
}

func TestActivityProgressIsThrottledAndCannotReopenFinishedLease(t *testing.T) {
	var updates []api.ActivityEvent
	lease := &activityLease{
		event:   api.ActivityEvent{ActivityID: "search-1", Status: api.ActivityStatusActive},
		publish: func(event api.ActivityEvent) { updates = append(updates, event) },
	}
	lease.report(api.ToolProgress{Phase: "selecting"})
	lease.report(api.ToolProgress{Phase: "searching", FilesSearched: 1})
	lease.report(api.ToolProgress{Phase: "searching", FilesSearched: 2})
	if len(updates) != 2 {
		t.Fatalf("unthrottled progress: %d events", len(updates))
	}
	lease.lastProgress = time.Time{}
	lease.report(api.ToolProgress{Phase: "searching", FilesSearched: 3})
	lease.finish()
	lease.report(api.ToolProgress{Phase: "searching", FilesSearched: 4})
	lease.finish()
	if len(updates) != 4 || updates[3].Status != api.ActivityStatusDone || updates[3].Progress.FilesSearched != 3 {
		t.Fatalf("activity updates: %+v", updates)
	}
	if updates[1].Progress.FilesSearched != 1 {
		t.Fatal("previously published progress was mutated")
	}
}

func eventFixtureProject(t *testing.T) string {
	t.Helper()
	registry := project.NewMemoryRegistry()
	p, err := registry.Create(t.Context(), project.CreateParams{Draft: true, Name: "Prompt event fixture"})
	testutil.FailErr(t, "register prompt event project", err)
	return p.ID
}
