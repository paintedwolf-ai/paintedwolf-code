// Package eventoutbox delivers events committed with durable mutations.
package eventoutbox

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/pkg/api"
)

type row struct {
	id        int64
	eventID   string
	revision  uint64
	topic     api.EventTopic
	projectID string
	sessionID string
	facet     string
	data      json.RawMessage
}

// DeliveredEvent is the committed lifecycle edge that reached the local hub.
type DeliveredEvent struct {
	Topic api.EventTopic
	Data  json.RawMessage
}

// Outbox persists and delivers events.
type Outbox struct {
	db     db.Handle
	hub    events.EventHub
	wake   chan struct{}
	syncCh chan chan error
	// idle bounds how long delivery waits for a wake before sweeping anyway, so a
	// lost Notify is a delay rather than a stall.
	idle time.Duration
	// lifecycleMu guards cancel and stopped between concurrent Start, Wait, and Close calls.
	lifecycleMu sync.Mutex
	drainMu     sync.Mutex
	cancel      context.CancelFunc
	stopped     chan struct{}
	wg          sync.WaitGroup
	epoch       atomic.Uint64
	draining    atomic.Bool
	// OnDelivered runs after a row reaches the hub.
	OnDelivered func(context.Context, DeliveredEvent)
}

// Epoch increases whenever a row is enqueued or processed by the outbox, so a
// fixed-point drain can tell whether a pass produced or delivered outbox work.
func (o *Outbox) Epoch() uint64 {
	if o == nil {
		return 0
	}
	return o.epoch.Load()
}

// Busy reports whether the outbox is currently draining rows.
func (o *Outbox) Busy() bool {
	if o == nil {
		return false
	}
	return o.draining.Load()
}

// idleSweep is the delivery sweep every host runs without a wake.
const idleSweep = time.Second

func New(database db.Handle, hub events.EventHub) *Outbox {
	return &Outbox{db: database, hub: hub, wake: make(chan struct{}, 1), syncCh: make(chan chan error)}
}

// ErrNotWired reports an enqueue against an outbox that was never constructed.
var ErrNotWired = errors.New("event outbox: not wired")

// EnqueueTx records an event in the caller's transaction. Nothing is delivered
// until that transaction commits; call Notify afterwards to wake delivery.
func (o *Outbox) EnqueueTx(ctx context.Context, tx *sql.Tx, topic api.EventTopic, key events.PublishKey, data any) error {
	// A host without an outbox has no subscribers; the event drops with a warning.
	if o == nil {
		warnUnwired(ctx, topic)
		return nil
	}
	if tx == nil {
		return fmt.Errorf("event outbox: %s enqueue requires a transaction", topic)
	}
	if strings.TrimSpace(string(topic)) == "" {
		return fmt.Errorf("event outbox: enqueue requires a topic")
	}
	if err := events.ValidatePublishScope(topic, key); err != nil {
		return fmt.Errorf("event outbox: %w", err)
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	eventID := strings.TrimSpace(key.EventID)
	if eventID == "" {
		eventID = uuid.NewString()
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO event_outbox(event_id, topic, project_id, session_id, facet, entity_revision, data_json, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		eventID, string(topic), key.Project, key.Session, key.Facet, key.EntityRevision, string(raw), time.Now().UTC().Format(time.RFC3339Nano))
	if err == nil {
		o.epoch.Add(1)
	}
	return err
}

// unwiredTopics remembers which topics already reported a missing outbox, so an
// unwired host says so once per topic instead of once per mutation.
var unwiredTopics sync.Map

func warnUnwired(ctx context.Context, topic api.EventTopic) {
	if _, seen := unwiredTopics.LoadOrStore(topic, true); seen {
		return
	}
	slog.WarnContext(ctx, "event outbox not wired: dropping events", "topic", topic)
}

// EnqueueProjectTx records an authoritative project change in the caller's transaction.
func (o *Outbox) EnqueueProjectTx(ctx context.Context, tx *sql.Tx, projectID string, data any) error {
	return o.EnqueueTx(ctx, tx, api.EventTopicProject, events.PublishKey{Project: projectID}, data)
}

// Enqueue records an event in a transaction of its own and wakes delivery, for
// producers with no transaction to join.
func (o *Outbox) Enqueue(ctx context.Context, topic api.EventTopic, key events.PublishKey, data any) error {
	if o == nil || o.db == nil {
		return ErrNotWired
	}
	tx, err := o.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := o.EnqueueTx(ctx, tx, topic, key, data); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	o.Notify()
	return nil
}

// Notify schedules delivery after the caller commits.
func (o *Outbox) Notify() {
	if o == nil {
		return
	}
	o.epoch.Add(1)
	select {
	case o.wake <- struct{}{}:
	default:
	}
}

// Start begins one ordered dispatcher.
func (o *Outbox) Start(parent context.Context) {
	if o == nil || o.db == nil || o.hub == nil {
		return
	}
	o.lifecycleMu.Lock()
	if o.cancel != nil {
		o.lifecycleMu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(parent)
	stopped := make(chan struct{})
	o.cancel = cancel
	o.stopped = stopped
	o.wg.Add(1)
	o.lifecycleMu.Unlock()
	go o.run(ctx, stopped)
	o.Notify()
}

// Wait blocks until the dispatcher has drained every currently committed outbox
// row, including hub publication, OnDelivered callbacks, and row deletion.
func (o *Outbox) Wait(ctx context.Context) error {
	if o == nil || o.db == nil || o.hub == nil {
		return nil
	}
	o.lifecycleMu.Lock()
	started := o.cancel != nil
	stopped := o.stopped
	o.lifecycleMu.Unlock()
	if !started {
		return o.drain(ctx)
	}
	reply := make(chan error, 1)
	select {
	case o.syncCh <- reply:
	case <-stopped:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-reply:
		return err
	case <-stopped:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (o *Outbox) run(ctx context.Context, stopped chan struct{}) {
	defer close(stopped)
	defer o.wg.Done()
	idle := o.idle
	if idle <= 0 {
		idle = idleSweep
	}
	ticker := time.NewTicker(idle)
	defer ticker.Stop()
	for {
		if err := o.drain(ctx); err != nil && !errors.Is(err, context.Canceled) {
			slog.WarnContext(ctx, "deliver event outbox", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case reply := <-o.syncCh:
			reply <- o.drain(ctx)
		case <-o.wake:
		case <-ticker.C:
		}
	}
}

// A crash after publish and before deletion can replay the same event ID.
func (o *Outbox) drain(ctx context.Context) error {
	o.drainMu.Lock()
	o.draining.Store(true)
	defer func() {
		o.draining.Store(false)
		o.drainMu.Unlock()
	}()
	for {
		entry, ok, err := o.next(ctx)
		if err != nil || !ok {
			return err
		}
		o.epoch.Add(1)
		key := events.PublishKey{Project: entry.projectID, Session: entry.sessionID, Facet: entry.facet, EventID: entry.eventID, EntityRevision: entry.revision}
		if entry.topic == "" || !json.Valid(entry.data) || events.ValidatePublishScope(entry.topic, key) != nil {
			if err := o.remove(ctx, entry.id); err != nil {
				return err
			}
			slog.ErrorContext(ctx, "discarded malformed event outbox row", "id", entry.id, "event_id", entry.eventID)
			continue
		}
		if err := o.hub.Publish(ctx, entry.topic, key, entry.data); err != nil {
			_, recordErr := o.db.ExecContext(ctx,
				`UPDATE event_outbox SET attempts = attempts + 1, last_error = ? WHERE id = ?`, err.Error(), entry.id)
			return errors.Join(err, recordErr)
		}
		if o.OnDelivered != nil {
			o.OnDelivered(ctx, DeliveredEvent{Topic: entry.topic, Data: entry.data})
		}
		if err := o.remove(ctx, entry.id); err != nil {
			return err
		}
	}
}

func (o *Outbox) remove(ctx context.Context, id int64) error {
	_, err := o.db.ExecContext(ctx, `DELETE FROM event_outbox WHERE id = ?`, id)
	return err
}

func (o *Outbox) next(ctx context.Context) (row, bool, error) {
	var entry row
	var topic, data string
	err := o.db.QueryRowContext(ctx, `SELECT id, event_id, topic, project_id, COALESCE(session_id, ''), COALESCE(facet, ''), entity_revision, data_json FROM event_outbox ORDER BY id LIMIT 1`).
		Scan(&entry.id, &entry.eventID, &topic, &entry.projectID, &entry.sessionID, &entry.facet, &entry.revision, &data)
	if errors.Is(err, sql.ErrNoRows) {
		return row{}, false, nil
	}
	if err != nil {
		return row{}, false, err
	}
	entry.topic = api.EventTopic(topic)
	entry.data = json.RawMessage(data)
	return entry, true, nil
}

func (o *Outbox) Close() error {
	if o == nil {
		return nil
	}
	o.lifecycleMu.Lock()
	cancel := o.cancel
	o.lifecycleMu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	o.wg.Wait()
	return nil
}
