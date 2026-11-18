package wiring

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/tools/toolstest"

	"github.com/lycaon/lycaon/internal/eventoutbox"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// Project hub subscribers never observe a worker child's live bodies. Live
// tokens ride the session stream; durable append and settle stay on
// GET /v1/events.
func TestParentOnlyProjectSubscriberCannotSeeWorkerLiveBodies(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "live-planes.db")
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, t.TempDir())

	hub := events.NewMemoryHub()
	outbox := eventoutbox.New(sqlDB, hub)
	outbox.Start(t.Context())
	t.Cleanup(func() { _ = outbox.Close() })

	st := store.NewSQL(sqlDB)
	st.SetEventOutbox(outbox)
	mgr := session.NewHost(st, session.Models{Limits: settings.DefaultSessionLimits()}, toolstest.NewRegistry())
	mgr.SetEventPublisher(&events.Publisher{Hub: hub})

	ctx := context.Background()
	parent, err := st.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create parent", err)
	child, err := st.CreateChild(ctx, parent, api.SpawnChildRequest{AgentType: "implementer"})
	testutil.FailErr(t, "create child", err)

	ch, unsub, err := hub.Subscribe(ctx, events.Subscription{Project: parent.ProjectID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe", err)
	t.Cleanup(unsub)

	// awaitAppend reads the project stream until the durable append of id,
	// returning every message event seen before it.
	awaitAppend := func(id string) []api.MessageEvent {
		var seen []api.MessageEvent
		for {
			hub.FlushDebounced()
			select {
			case env := <-ch:
				if env.Topic != api.EventTopicMessage {
					continue
				}
				var ev api.MessageEvent
				testutil.FailErr(t, "decode message event", json.Unmarshal(env.Data, &ev))
				if ev.Op == api.MessageChangeAppend && ev.Message.ID == id {
					return seen
				}
				seen = append(seen, ev)
			case <-t.Context().Done():
				t.Fatalf("project stream never delivered the append of %s", id)
			}
		}
	}
	appendInternal := func(id string) {
		testutil.FailErr(t, "append "+id, st.AppendMessages(ctx, child.ID, api.Message{
			ID: id, Role: api.MessageRoleAssistant, Visibility: api.MessageVisibilityInternal, CreatedAt: time.Now().UTC(),
		}))
	}

	const liveID = "child-live-1"
	appendInternal(liveID)
	awaitAppend(liveID)

	liveCh, liveUnsub := mgr.Runner.Transcript.Streams.Subscribe(liveID)
	t.Cleanup(liveUnsub)
	const frames = 30
	for i := range frames {
		testutil.FailErr(t, "child live", mgr.Runner.Transcript.Streams.Project(ctx, child.ID, api.Message{
			ID: liveID, Role: api.MessageRoleAssistant, Content: "worker-scratch-" + strconv.Itoa(i),
		}))
	}
	mgr.Runner.Transcript.Streams.Flush(ctx, child.ID)

	// A durable sentinel appended after the live frames bounds the window in
	// which a leaked live patch would reach the project stream.
	const sentinelID = "child-durable-sentinel"
	appendInternal(sentinelID)
	for _, ev := range awaitAppend(sentinelID) {
		if ev.SessionID == child.ID && ev.Op == api.MessageChangePatch {
			t.Fatalf("project stream saw a child live patch: content=%q", ev.Message.Content)
		}
	}

	select {
	case frame := <-liveCh:
		if frame.Content == "" {
			t.Fatalf("session stream frame = %+v, want child live content", frame)
		}
	case <-t.Context().Done():
		t.Fatal("session stream did not receive child live content")
	}
}
