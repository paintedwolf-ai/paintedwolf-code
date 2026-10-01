package app

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func waitMessageAppend(t *testing.T, ch <-chan wire.EventEnvelope, sessionID string) wire.MessageEvent {
	t.Helper()
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
				testutil.FailErr(t, "unmarshal message event", err)
			}
			if ev.SessionID != sessionID || ev.Op != wire.MessageChangeAppend {
				continue
			}
			return ev
		case <-deadline:
			t.Fatal("timeout waiting for message append SSE")
			return wire.MessageEvent{}
		}
	}
}

// SSE publish must carry the AppendMessages-minted ord/seq (not a pre-append copy).
func TestProgressUpdateSSECarriesMintedOrd(t *testing.T) {
	store, sessionID, sqlDB, prog := newStampStore(t)
	const runID = "run-sse-update"
	seedWorkflowRun(t, sqlDB, sessionID, runID)

	hub := events.NewMemoryHub()
	pub := &events.Publisher{Hub: hub}
	sess, err := store.Get(context.Background(), sessionID)
	testutil.FailErr(t, "store.Get", err)
	ch, unsub, err := hub.Subscribe(context.Background(), events.Subscription{Project: sess.ProjectID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "hub.Subscribe", err)
	t.Cleanup(unsub)

	flush := newProgressChangeEmitter(store, pub, fixedActiveRun(runID), prog)
	flush(progress.FlushPayload{
		SessionID: sessionID,
		Baseline:  "",
		Latest:    "- [ ] a\n- [ ] b\n",
		Seq:       1,
		ChangedAt: time.Now().UTC(),
	})

	ev := waitMessageAppend(t, ch, sessionID)
	if ev.Message.Kind != wire.MessageKindProgressUpdate {
		t.Fatalf("kind = %q want progress_update", ev.Message.Kind)
	}
	if ev.Message.Ord <= 0 {
		t.Fatalf("SSE progress_update ord = %d want > 0", ev.Message.Ord)
	}
	if ev.Message.Seq <= 0 {
		t.Fatalf("SSE progress_update seq = %d want > 0", ev.Message.Seq)
	}

	stored := findMessageByKind(t, mustGetMessages(t, store, sessionID), wire.MessageKindProgressUpdate)
	if ev.Message.Ord != stored.Ord || ev.Message.Seq != stored.Seq {
		t.Fatalf("SSE clocks (ord=%d seq=%d) != store (ord=%d seq=%d)",
			ev.Message.Ord, ev.Message.Seq, stored.Ord, stored.Seq)
	}
}

// Same ord contract for progress_complete.
func TestProgressCompleteSSECarriesMintedOrd(t *testing.T) {
	store, sessionID, sqlDB, prog := newStampStore(t)
	const runID = "run-sse-complete"
	seedWorkflowRun(t, sqlDB, sessionID, runID)
	prog.Set(sessionID, "- [x] a\n- [x] b\n")

	hub := events.NewMemoryHub()
	pub := &events.Publisher{Hub: hub}
	sess, err := store.Get(context.Background(), sessionID)
	testutil.FailErr(t, "store.Get", err)
	ch, unsub, err := hub.Subscribe(context.Background(), events.Subscription{Project: sess.ProjectID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "hub.Subscribe", err)
	t.Cleanup(unsub)

	emitProgressCompletion(context.Background(), store, pub, prog, fixedActiveRun(runID), sessionID)

	ev := waitMessageAppend(t, ch, sessionID)
	if ev.Message.Kind != wire.MessageKindProgressComplete {
		t.Fatalf("kind = %q want progress_complete", ev.Message.Kind)
	}
	if ev.Message.Ord <= 0 {
		t.Fatalf("SSE progress_complete ord = %d want > 0", ev.Message.Ord)
	}
	stored := findMessageByKind(t, mustGetMessages(t, store, sessionID), wire.MessageKindProgressComplete)
	if ev.Message.Ord != stored.Ord {
		t.Fatalf("SSE ord %d != store ord %d", ev.Message.Ord, stored.Ord)
	}
}

func mustGetMessages(t *testing.T, store interface {
	GetMessages(context.Context, string) ([]wire.Message, error)
}, sessionID string) []wire.Message {
	t.Helper()
	msgs, err := store.GetMessages(context.Background(), sessionID)
	testutil.FailErr(t, "GetMessages", err)
	return msgs
}
