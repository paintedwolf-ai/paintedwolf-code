package eventoutbox

import (
	"bytes"
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

const testTopic = api.EventTopicProject

func testDB(t *testing.T) db.Handle {
	t.Helper()
	sqlDB := testdbfixture.Open(t, "store.db")
	return sqlDB
}

func outboxRows(t *testing.T, sqlDB db.Handle) int {
	t.Helper()
	var n int
	testutil.FailErr(t, "count outbox rows",
		sqlDB.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM event_outbox`).Scan(&n))
	return n
}

// recordingHub captures deliveries and can fail a chosen number of them, which
// is how a crash between Publish and the DELETE is reproduced.
type recordingHub struct {
	mu        sync.Mutex
	published []string
	failures  int
}

func (h *recordingHub) Publish(_ context.Context, _ api.EventTopic, key events.PublishKey, _ any) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.published = append(h.published, key.EventID)
	if h.failures > 0 {
		h.failures--
		return errors.New("subscriber fanout failed")
	}
	return nil
}

func (h *recordingHub) Subscribe(context.Context, events.Subscription) (<-chan api.EventEnvelope, func(), error) {
	return nil, func() {}, nil
}

func (h *recordingHub) SubscriberCount() int { return 0 }

func (h *recordingHub) deliveries() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.published...)
}

// The event is as durable as the mutation it describes, and no more: a caller
// that rolls back takes its event with it.
func TestEnqueueTxJoinsTheCallersTransaction(t *testing.T) {
	sqlDB := testDB(t)
	outbox := New(sqlDB, &recordingHub{})

	tx, err := sqlDB.BeginTx(t.Context(), nil)
	testutil.FailErr(t, "begin rolled-back tx", err)
	testutil.FailErr(t, "enqueue in rolled-back tx",
		outbox.EnqueueTx(t.Context(), tx, testTopic, events.PublishKey{Project: "018eca20-5608-7a55-824f-920998f472b2", EventID: "ev-rollback"}, map[string]string{"op": "noop"}))
	testutil.FailErr(t, "rollback", tx.Rollback())
	if n := outboxRows(t, sqlDB); n != 0 {
		t.Fatalf("outbox rows = %d want 0 after the caller rolled back", n)
	}

	tx, err = sqlDB.BeginTx(t.Context(), nil)
	testutil.FailErr(t, "begin committed tx", err)
	testutil.FailErr(t, "enqueue in committed tx",
		outbox.EnqueueTx(t.Context(), tx, testTopic, events.PublishKey{Project: "018eca20-5608-7a55-824f-920998f472b2", EventID: "ev-commit"}, map[string]string{"op": "noop"}))
	testutil.FailErr(t, "commit", tx.Commit())
	if n := outboxRows(t, sqlDB); n != 1 {
		t.Fatalf("outbox rows = %d want 1 after the caller committed", n)
	}
}

func TestEnqueueTxRejectsANilTransaction(t *testing.T) {
	outbox := New(testDB(t), &recordingHub{})
	err := outbox.EnqueueTx(t.Context(), nil, testTopic, events.PublishKey{Project: "018eca20-5608-7a55-824f-920998f472b2"}, map[string]string{})
	if err == nil {
		t.Fatal("enqueue with no transaction succeeded")
	}
	if !strings.Contains(err.Error(), "transaction") {
		t.Fatalf("error = %v want it to name the missing transaction", err)
	}
}

// An unwired outbox must report dropped events.
func TestEnqueueTxOnAnUnwiredOutboxSaysSo(t *testing.T) {
	var logged bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	// A topic no other test uses, so the once-per-topic warning is this one.
	// warnUnwired's guard is package-level; clear it or -count>1 reruns see
	// the topic as already logged.
	const topic = api.EventTopic("test.unwired")
	t.Cleanup(func() { unwiredTopics.Delete(topic) })
	var absent *Outbox
	testutil.FailErr(t, "enqueue on an unwired outbox",
		absent.EnqueueTx(t.Context(), nil, topic, events.PublishKey{Project: "018eca20-5608-7a55-824f-920998f472b2"}, map[string]string{}))
	if !strings.Contains(logged.String(), string(topic)) {
		t.Fatalf("unwired enqueue logged %q, want a warning naming %s", logged.String(), topic)
	}
}

// Publish-then-delete is the delivery guarantee. A failure after the subscriber
// fanout leaves the row, so the next drain sends it again: at-least-once, never
// at-most-once.
func TestDrainRepublishesRatherThanLosingAnEvent(t *testing.T) {
	sqlDB := testDB(t)
	hub := &recordingHub{failures: 1}
	outbox := New(sqlDB, hub)
	testutil.FailErr(t, "enqueue",
		outbox.Enqueue(t.Context(), testTopic, events.PublishKey{Project: "018eca20-5608-7a55-824f-920998f472b2", EventID: "ev-1"}, map[string]string{"op": "noop"}))

	if err := outbox.drain(t.Context()); err == nil {
		t.Fatal("drain reported success while delivery failed")
	}
	if n := outboxRows(t, sqlDB); n != 1 {
		t.Fatalf("outbox rows = %d want the undelivered event kept", n)
	}
	testutil.FailErr(t, "second drain", outbox.drain(t.Context()))
	if got := hub.deliveries(); len(got) != 2 || got[0] != "ev-1" || got[1] != "ev-1" {
		t.Fatalf("deliveries = %v want the same event published twice", got)
	}
	if n := outboxRows(t, sqlDB); n != 0 {
		t.Fatalf("outbox rows = %d want 0 once delivery succeeded", n)
	}
}

// Delivery order is the row order, so an entity's transitions reach a subscriber
// in the order they committed.
func TestDrainPublishesInCommitOrder(t *testing.T) {
	sqlDB := testDB(t)
	hub := &recordingHub{}
	outbox := New(sqlDB, hub)
	for _, id := range []string{"ev-1", "ev-2", "ev-3"} {
		testutil.FailErr(t, "enqueue "+id,
			outbox.Enqueue(t.Context(), testTopic, events.PublishKey{Project: "018eca20-5608-7a55-824f-920998f472b2", EventID: id}, map[string]string{"op": id}))
	}
	testutil.FailErr(t, "drain", outbox.drain(t.Context()))
	got := hub.deliveries()
	if len(got) != 3 || got[0] != "ev-1" || got[1] != "ev-2" || got[2] != "ev-3" {
		t.Fatalf("deliveries = %v want commit order", got)
	}
}

// With the idle sweep an hour out, only Notify can deliver the committed row.
func TestNotifyWakesTheDispatcher(t *testing.T) {
	sqlDB := testDB(t)
	hub := &recordingHub{}
	outbox := New(sqlDB, hub)
	outbox.idle = time.Hour
	outbox.Start(t.Context())
	t.Cleanup(func() { _ = outbox.Close() })
	// Let the startup sweep finish, so the dispatcher is parked on its wake.
	time.Sleep(100 * time.Millisecond)

	tx, err := sqlDB.BeginTx(t.Context(), nil)
	testutil.FailErr(t, "begin", err)
	testutil.FailErr(t, "enqueue",
		outbox.EnqueueTx(t.Context(), tx, testTopic, events.PublishKey{Project: "018eca20-5608-7a55-824f-920998f472b2", EventID: "ev-wake"}, map[string]string{"op": "noop"}))
	testutil.FailErr(t, "commit", tx.Commit())

	time.Sleep(100 * time.Millisecond)
	if got := hub.deliveries(); len(got) != 0 {
		t.Fatalf("deliveries = %v want none before Notify", got)
	}
	outbox.Notify()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(hub.deliveries()) == 1 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("Notify did not wake delivery: deliveries = %v", hub.deliveries())
}

func TestEnqueueWithoutADatabaseReportsNotWired(t *testing.T) {
	outbox := New(nil, &recordingHub{})
	if err := outbox.Enqueue(t.Context(), testTopic, events.PublishKey{Project: "018eca20-5608-7a55-824f-920998f472b2"}, map[string]string{}); !errors.Is(err, ErrNotWired) {
		t.Fatalf("Enqueue error = %v want ErrNotWired", err)
	}
}

func TestDrainCallsOnDeliveredAfterPublish(t *testing.T) {
	sqlDB := testDB(t)
	hub := &recordingHub{}
	outbox := New(sqlDB, hub)
	var got []api.EventTopic
	outbox.OnDelivered = func(_ context.Context, delivered DeliveredEvent) {
		got = append(got, delivered.Topic)
	}
	testutil.FailErr(t, "enqueue",
		outbox.Enqueue(t.Context(), testTopic, events.PublishKey{Project: "018eca20-5608-7a55-824f-920998f472b2", EventID: "ev-1"}, map[string]string{}))
	testutil.FailErr(t, "drain", outbox.drain(t.Context()))
	if len(got) != 1 || got[0] != testTopic {
		t.Fatalf("OnDelivered topics = %v want [%s]", got, testTopic)
	}
}

// Reject invalid scope before it can block FIFO delivery.
func TestEnqueueTxRejectsRowsThatFailScopeValidation(t *testing.T) {
	sqlDB := testDB(t)
	outbox := New(sqlDB, &recordingHub{})

	tx, err := sqlDB.BeginTx(t.Context(), nil)
	testutil.FailErr(t, "begin", err)
	defer func() { _ = tx.Rollback() }()

	// api.EventTopicMessage requires project scope; a session-only key has none.
	err = outbox.EnqueueTx(t.Context(), tx, api.EventTopicMessage, events.PublishKey{Session: "sess-1"}, map[string]string{})
	if err == nil {
		t.Fatal("enqueue with session-only scope on a project-scoped topic succeeded")
	}
	if !strings.Contains(err.Error(), "project scope") {
		t.Fatalf("error = %v want it to name the missing project scope", err)
	}
	testutil.FailErr(t, "rollback", tx.Rollback())
	if n := outboxRows(t, sqlDB); n != 0 {
		t.Fatalf("outbox rows = %d want 0 — the invalid row must never be inserted", n)
	}
}

// An empty-topic row would block the queue head, so it never reaches the table.
func TestEnqueueTxRejectsEmptyTopic(t *testing.T) {
	sqlDB := testDB(t)
	outbox := New(sqlDB, &recordingHub{})

	tx, err := sqlDB.BeginTx(t.Context(), nil)
	testutil.FailErr(t, "begin", err)
	defer func() { _ = tx.Rollback() }()

	err = outbox.EnqueueTx(t.Context(), tx, api.EventTopic(""), events.PublishKey{Project: "018eca20-5608-7a55-824f-920998f472b2"}, map[string]string{})
	if err == nil {
		t.Fatal("enqueue with an empty topic succeeded")
	}
}

// selectiveFailHub always fails one chosen event id and delivers everything
// else, which is how a permanently unpublishable row is told apart from an
// ordinary transient failure.
type selectiveFailHub struct {
	mu          sync.Mutex
	failEventID string
	attempts    int
	published   []string
}

func (h *selectiveFailHub) Publish(_ context.Context, _ api.EventTopic, key events.PublishKey, _ any) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if key.EventID == h.failEventID {
		h.attempts++
		return errors.New("permanently broken subscriber")
	}
	h.published = append(h.published, key.EventID)
	return nil
}

func (h *selectiveFailHub) Subscribe(context.Context, events.Subscription) (<-chan api.EventEnvelope, func(), error) {
	return nil, func() {}, nil
}

func (h *selectiveFailHub) SubscriberCount() int { return 0 }

func (h *selectiveFailHub) deliveries() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.published...)
}

// A row that can never deliver must not wedge the rows behind it forever: it
// blocks them only while under the retry cap, and drain keeps flowing again
// once the row is dropped past it.
func TestDrainDropsRowAfterMaxDeliveryAttempts(t *testing.T) {
	var logged bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelError})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	sqlDB := testDB(t)
	hub := &selectiveFailHub{failEventID: "ev-bad"}
	outbox := New(sqlDB, hub)

	testutil.FailErr(t, "enqueue bad",
		outbox.Enqueue(t.Context(), testTopic, events.PublishKey{Project: "018eca20-5608-7a55-824f-920998f472b2", EventID: "ev-bad"}, map[string]string{}))
	testutil.FailErr(t, "enqueue good",
		outbox.Enqueue(t.Context(), testTopic, events.PublishKey{Project: "018eca20-5608-7a55-824f-920998f472b2", EventID: "ev-good"}, map[string]string{}))

	// Under the cap, the bad row still blocks the good one queued behind it.
	for i := 0; i < maxDeliveryAttempts-1; i++ {
		if err := outbox.drain(t.Context()); err == nil {
			t.Fatalf("drain attempt %d: want failure while under the retry cap", i)
		}
	}
	if got := hub.deliveries(); len(got) != 0 {
		t.Fatalf("deliveries = %v want none while the bad row blocks the queue", got)
	}
	if n := outboxRows(t, sqlDB); n != 2 {
		t.Fatalf("outbox rows = %d want both rows still queued", n)
	}

	// The attempt that exceeds the cap drops the bad row and keeps draining
	// straight through to the good one in the same call.
	testutil.FailErr(t, "final drain", outbox.drain(t.Context()))
	if got := hub.deliveries(); len(got) != 1 || got[0] != "ev-good" {
		t.Fatalf("deliveries = %v want only ev-good delivered", got)
	}
	if n := outboxRows(t, sqlDB); n != 0 {
		t.Fatalf("outbox rows = %d want both rows gone (one delivered, one dropped)", n)
	}
	if hub.attempts != maxDeliveryAttempts {
		t.Fatalf("hub saw %d attempts at the bad row, want exactly %d", hub.attempts, maxDeliveryAttempts)
	}
	if !strings.Contains(logged.String(), "ev-bad") {
		t.Fatalf("dropped row was not logged loudly: %q", logged.String())
	}
}

// Concurrent Start calls run one dispatcher; -race checks the cancel guard.
func TestStartIsSafeUnderConcurrentCalls(t *testing.T) {
	sqlDB := testDB(t)
	hub := &recordingHub{}
	outbox := New(sqlDB, hub)
	outbox.idle = time.Hour

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			outbox.Start(t.Context())
		}()
	}
	wg.Wait()
	t.Cleanup(func() { testutil.FailErr(t, "close", outbox.Close()) })

	testutil.FailErr(t, "enqueue",
		outbox.Enqueue(t.Context(), testTopic, events.PublishKey{Project: "018eca20-5608-7a55-824f-920998f472b2", EventID: "ev-once"}, map[string]string{}))
	outbox.Notify()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(hub.deliveries()) == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	// Give a stray second dispatcher a chance to double-deliver before checking.
	time.Sleep(50 * time.Millisecond)
	if got := hub.deliveries(); len(got) != 1 {
		t.Fatalf("deliveries = %v want exactly one dispatcher delivering exactly once", got)
	}
}
