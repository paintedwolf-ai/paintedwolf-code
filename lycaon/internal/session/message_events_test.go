package session_test

import (
	"context"
	"encoding/json"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	wire "github.com/lycaon/lycaon/pkg/api"
	"testing"
	"time"
)

func TestAppendMessagesPublishesMessageSSE(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "message-sse.db")

	store := store.NewSQL(sqlDB)
	hub := events.NewMemoryHub()
	pub := &events.Publisher{Hub: hub}
	mgr := session.NewManager(store, nil, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	mgr.SetEventPublisher(pub)

	ctx := context.Background()
	dir := t.TempDir()

	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)

	sess, err := store.Create(ctx, wire.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session failed", err)
	child, err := store.CreateChild(ctx, sess, wire.SpawnChildRequest{AgentType: "implementer"})
	testutil.FailErr(t, "create child", err)

	subCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	ch, unsub, err := hub.Subscribe(subCtx, events.Subscription{Project: sess.ProjectID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe failed", err)
	defer unsub()

	if _, err := mgr.AppendWorkerSummary(ctx, sess.ID, session.WorkerSummaryInput{
		Summary: "worker done", JobID: "job-sse", ChildSessionID: child.ID, AgentType: "implementer",
	}); err != nil {
		testutil.FailErr(t, "mgr.AppendWorkerSummary failed", err)
	}

	deadline := time.After(2 * time.Second)
	for {
		select {
		case env, ok := <-ch:
			if !ok {
				t.Fatal("hub closed")
			}
			if env.Topic != wire.EventTopicMessage {
				continue
			}
			var ev wire.MessageEvent
			if err := json.Unmarshal(env.Data, &ev); err != nil {
				testutil.FailErr(t, "unmarshal JSON document", err)
			}
			if ev.SessionID != sess.ID {
				continue
			}
			if ev.Message.WorkerSummary != nil {
				return
			}
		case <-deadline:
			t.Fatal("timeout waiting for message topic event")
		}
	}
}
