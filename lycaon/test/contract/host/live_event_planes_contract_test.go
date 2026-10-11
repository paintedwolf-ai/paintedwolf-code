package contract_test

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/eventoutbox"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// Parent-only project hub subscribers must never observe worker-child live
// bodies. Live tokens ride the session stream; durable append/settle stay on
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
	pub := &events.Publisher{Hub: hub}
	mgr := session.NewHost(st, session.Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	mgr.SetEventPublisher(pub)

	ctx := context.Background()
	parent, err := st.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create parent", err)
	child, err := st.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create child", err)
	_ = parent

	ch, unsub, err := hub.Subscribe(ctx, events.Subscription{Project: child.ProjectID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe", err)
	t.Cleanup(unsub)

	drain := func(wait time.Duration) []api.MessageEvent {
		var out []api.MessageEvent
		deadline := time.After(wait)
		for {
			select {
			case env := <-ch:
				if env.Topic != api.EventTopicMessage {
					continue
				}
				var ev api.MessageEvent
				if err := json.Unmarshal(env.Data, &ev); err != nil {
					testutil.FailErr(t, "decode", err)
				}
				out = append(out, ev)
			case <-deadline:
				return out
			}
		}
	}
	_ = drain(50 * time.Millisecond)

	childMsg := "child-live-1"
	testutil.FailErr(t, "append child placeholder", st.AppendMessages(ctx, child.ID, api.Message{
		ID:         childMsg,
		Role:       api.MessageRoleAssistant,
		Visibility: api.MessageVisibilityInternal,
		CreatedAt:  time.Now().UTC(),
	}))
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var count int
		if err := sqlDB.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM event_outbox`).Scan(&count); err == nil && count == 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	_ = drain(200 * time.Millisecond)

	liveCh, liveUnsub := mgr.Runner.Transcript.Streams.Subscribe(childMsg)
	t.Cleanup(liveUnsub)

	for i := 0; i < 30; i++ {
		testutil.FailErr(t, "child live", mgr.Runner.Transcript.Streams.Project(ctx, child.ID, api.Message{
			ID:      childMsg,
			Role:    api.MessageRoleAssistant,
			Content: "worker-scratch-" + strconv.Itoa(i),
		}))
	}
	mgr.Runner.Transcript.Streams.Flush(ctx, child.ID)

	hubAfterLive := drain(200 * time.Millisecond)
	for _, ev := range hubAfterLive {
		if ev.SessionID == child.ID && ev.Op == api.MessageChangePatch {
			t.Fatalf("parent-only hub saw child live patch: content=%q", ev.Message.Content)
		}
	}

	select {
	case frame := <-liveCh:
		if frame.Content == "" {
			t.Fatalf("expected live stream frame, got %+v", frame)
		}
	case <-time.After(time.Second):
		t.Fatal("painted session stream did not receive child live content")
	}
}
