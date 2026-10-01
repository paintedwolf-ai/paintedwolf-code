package app

import (
	"context"
	"encoding/json"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/eventoutbox"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbseed"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestProgressCompleteRetriesAfterSkippedWrite(t *testing.T) {
	st, sessionID, sqlDB, prog := newStampStore(t)
	prog.Set(sessionID, "- [x] a\n")

	emitProgressCompletion(context.Background(), st, &events.Publisher{}, prog, fixedActiveRun(""), sessionID)
	const runID = "run-after-skip"
	seedWorkflowRun(t, sqlDB, sessionID, runID)
	emitProgressCompletion(context.Background(), st, &events.Publisher{}, prog, fixedActiveRun(runID), sessionID)

	msgs, err := st.GetMessages(context.Background(), sessionID)
	testutil.FailErr(t, "GetMessages", err)
	var completes int
	for _, m := range msgs {
		if m.Kind == wire.MessageKindProgressComplete {
			completes++
		}
	}
	if completes != 1 {
		t.Fatalf("progress_complete rows = %d want 1 after skipped write then retry", completes)
	}
}

func TestProgressCompleteOutboxPublishesOnce(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "progress-outbox.db")
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, t.TempDir())

	hub := events.NewMemoryHub()
	ch, unsub, err := hub.Subscribe(context.Background(), events.Subscription{Project: testdbseed.DefaultProjectID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe", err)
	t.Cleanup(unsub)

	outbox := eventoutbox.New(sqlDB, hub)
	outbox.Start(t.Context())
	t.Cleanup(func() { outbox.Close() })

	st := store.NewSQL(sqlDB)
	st.SetEventOutbox(outbox)
	sess, err := st.Create(context.Background(), wire.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "store.Create", err)
	waitProgressOutboxEmpty(t, sqlDB)
	hub.FlushDebounced()
	drainUntilQuiet(t, ch, 50*time.Millisecond)

	const runID = "run-outbox-once"
	seedWorkflowRun(t, sqlDB, sess.ID, runID)
	prog := progress.NewSQLStore(sqlDB)
	prog.Set(sess.ID, "- [x] a\n- [x] b\n")

	pub := &events.Publisher{Hub: hub}
	emitProgressCompletion(context.Background(), st, pub, prog, fixedActiveRun(runID), sess.ID)
	waitProgressOutboxEmpty(t, sqlDB)
	hub.FlushDebounced()

	var appends []wire.MessageEvent
	deadline := time.After(500 * time.Millisecond)
	for {
		select {
		case env := <-ch:
			if env.Topic != wire.EventTopicMessage {
				continue
			}
			var ev wire.MessageEvent
			if err := json.Unmarshal(env.Data, &ev); err != nil {
				testutil.FailErr(t, "unmarshal message event", err)
			}
			if ev.Op == wire.MessageChangeAppend && ev.Message.Kind == wire.MessageKindProgressComplete {
				appends = append(appends, ev)
			}
		case <-deadline:
			goto done
		}
	}
done:
	if len(appends) != 1 {
		t.Fatalf("progress_complete SSE appends = %d want 1 (outbox delivers events)", len(appends))
	}
	msgs, err := st.GetMessages(context.Background(), sess.ID)
	testutil.FailErr(t, "GetMessages", err)
	var rows int
	for _, m := range msgs {
		if m.Kind == wire.MessageKindProgressComplete {
			rows++
		}
	}
	if rows != 1 {
		t.Fatalf("progress_complete rows = %d want 1", rows)
	}
}

func waitProgressOutboxEmpty(t *testing.T, database db.Handle) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var count int
		if err := database.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM event_outbox").Scan(&count); err == nil && count == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("event outbox did not drain")
}

func drainUntilQuiet(t *testing.T, ch <-chan wire.EventEnvelope, quiet time.Duration) {
	t.Helper()
	timer := time.NewTimer(quiet)
	defer timer.Stop()
	for {
		select {
		case <-ch:
			if !timer.Stop() {
				<-timer.C
			}
			timer.Reset(quiet)
		case <-timer.C:
			return
		}
	}
}
