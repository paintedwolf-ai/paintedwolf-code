package events

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/pkg/api"
)

const replayCapacity = 4096

type replayCursor struct {
	Generation string `json:"g"`
	Sequence   uint64 `json:"s"`
}

type subscriber struct {
	id      string
	project string
	viewer  people.Person
	ch      chan api.EventEnvelope
}

// receives reports whether env belongs on this subscriber's stream.
func (sub *subscriber) receives(env api.EventEnvelope) bool {
	return eventMatchesProject(env, sub.project) && people.MayObserveEvents(sub.viewer)
}

type debouncedEntry struct {
	env api.EventEnvelope

	// lifecycle is the session lifecycle state a coalesced session update carries.
	lifecycle *sessionLifecycle
}

// MemoryHub is an in-process EventHub with per-topic debouncing.
type MemoryHub struct {
	mu          sync.Mutex
	subscribers map[string]*subscriber
	timers      map[string]*time.Timer
	pending     map[string]debouncedEntry
	generation  string
	sequence    uint64
	replay      []api.EventEnvelope
	cursorKey   []byte

	// lifecycles is the last session lifecycle state delivered per session key.
	lifecycles map[string]sessionLifecycle
}

// NewMemoryHub creates an empty hub.
func NewMemoryHub() *MemoryHub {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic(err)
	}
	return &MemoryHub{
		subscribers: make(map[string]*subscriber),
		timers:      make(map[string]*time.Timer),
		pending:     make(map[string]debouncedEntry),
		lifecycles:  make(map[string]sessionLifecycle),
		generation:  uuid.NewString(),
		replay:      make([]api.EventEnvelope, 0, replayCapacity),
		cursorKey:   key,
	}
}

func (h *MemoryHub) encodeReplayCursor(cursor replayCursor) string {
	raw, err := json.Marshal(cursor)
	if err != nil {
		panic(err)
	}
	payload := base64.RawURLEncoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, h.cursorKey)
	_, _ = mac.Write([]byte(payload))
	return payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (h *MemoryHub) decodeReplayCursor(value string) (replayCursor, error) {
	parts := strings.Split(strings.TrimSpace(value), ".")
	if len(parts) != 2 {
		return replayCursor{}, fmt.Errorf("%w: invalid cursor", ErrReplayUnavailable)
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return replayCursor{}, fmt.Errorf("%w: invalid cursor", ErrReplayUnavailable)
	}
	mac := hmac.New(sha256.New, h.cursorKey)
	_, _ = mac.Write([]byte(parts[0]))
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return replayCursor{}, fmt.Errorf("%w: invalid cursor", ErrReplayUnavailable)
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return replayCursor{}, fmt.Errorf("%w: invalid cursor", ErrReplayUnavailable)
	}
	var cursor replayCursor
	if err := json.Unmarshal(raw, &cursor); err != nil || cursor.Generation == "" {
		return replayCursor{}, fmt.Errorf("%w: invalid cursor", ErrReplayUnavailable)
	}
	return cursor, nil
}

func debounceKey(topic api.EventTopic, key PublishKey) string {
	return fmt.Sprintf("%s:%s:%s:%s", topic, key.Project, key.Session, key.Facet)
}

// Publish emits an event to matching subscribers (debounced per topic window).
func (h *MemoryHub) Publish(ctx context.Context, topic api.EventTopic, key PublishKey, data any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ValidatePublishScope(topic, key); err != nil {
		return err
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	env := api.EventEnvelope{
		V:              1,
		EventID:        strings.TrimSpace(key.EventID),
		EntityRevision: key.EntityRevision,
		Topic:          topic,
		PublishedAt:    time.Now().UTC(),
		Scope:          key.Scope(),
		Data:           raw,
	}
	if env.EventID == "" {
		env.EventID = uuid.NewString()
	}
	window := DebounceForTopic(topic)
	if topic == api.EventTopicSession {
		h.publishSession(env, key, window)
		return nil
	}
	if window <= 0 {
		h.deliver(env)
		return nil
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	h.debounceLocked(debounceKey(topic, key), debouncedEntry{env: env}, window)
	return nil
}

// debounceLocked holds entry for window, replacing an older pending entry.
func (h *MemoryHub) debounceLocked(dkey string, entry debouncedEntry, window time.Duration) {
	if pending, ok := h.pending[dkey]; ok && pending.env.EntityRevision > 0 &&
		entry.env.EntityRevision > 0 && pending.env.EntityRevision > entry.env.EntityRevision {
		return
	}
	h.pending[dkey] = entry
	if timer, ok := h.timers[dkey]; ok {
		timer.Stop()
	}
	// Deliver only the timer still registered for this key.
	var timer *time.Timer
	timer = time.AfterFunc(window, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if current, ok := h.timers[dkey]; !ok || current != timer {
			return
		}
		entry, ok := h.pending[dkey]
		delete(h.pending, dkey)
		delete(h.timers, dkey)
		if !ok {
			return
		}
		h.deliverPendingLocked(dkey, entry)
	})
	h.timers[dkey] = timer
}

func (h *MemoryHub) deliverPendingLocked(dkey string, entry debouncedEntry) {
	h.deliverLocked(entry.env)
	if entry.lifecycle != nil {
		h.lifecycles[dkey] = *entry.lifecycle
	}
}

func (h *MemoryHub) FlushDebounced() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, timer := range h.timers {
		timer.Stop()
	}
	for dkey, entry := range h.pending {
		h.deliverPendingLocked(dkey, entry)
	}
	h.timers = make(map[string]*time.Timer)
	h.pending = make(map[string]debouncedEntry)
}

func (h *MemoryHub) deliver(env api.EventEnvelope) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.deliverLocked(env)
}

func (h *MemoryHub) deliverLocked(env api.EventEnvelope) {
	h.sequence++
	env.Cursor = h.encodeReplayCursor(replayCursor{Generation: h.generation, Sequence: h.sequence})
	h.replay = append(h.replay, env)
	if len(h.replay) > replayCapacity {
		copy(h.replay, h.replay[len(h.replay)-replayCapacity:])
		h.replay = h.replay[:replayCapacity]
	}
	var overflowed []string
	for _, sub := range h.subscribers {
		if !sub.receives(env) {
			continue
		}
		select {
		case sub.ch <- env:
		default:
			overflowed = append(overflowed, sub.id)
		}
	}
	// Drop slow subscribers so reconnect performs a full resync.
	for _, id := range overflowed {
		sub, ok := h.subscribers[id]
		if !ok {
			continue
		}
		delete(h.subscribers, id)
		close(sub.ch)
		slog.Warn("dropped SSE subscriber: event buffer full",
			"component", "events",
			"project_id", sub.project,
			"buffer_size", SubscriberBufferSize,
		)
	}
}

// CurrentCursor returns the latest delivered event boundary.
func (h *MemoryHub) CurrentCursor() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.encodeReplayCursor(replayCursor{Generation: h.generation, Sequence: h.sequence})
}

func eventMatchesProject(env api.EventEnvelope, project string) bool {
	// Every window renders the project registry, including projects it has not opened.
	return env.Topic == api.EventTopicProject || env.Scope.ProjectID == "" || project == "" || project == env.Scope.ProjectID
}

// Subscribe filters project content by project_id; every subscriber receives
// device events and project registry changes. An empty project receives
// everything the viewer may observe.
func (h *MemoryHub) Subscribe(ctx context.Context, want Subscription) (<-chan api.EventEnvelope, func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if !want.Viewer.Valid() {
		return nil, nil, ErrSubscriptionViewer
	}
	h.mu.Lock()
	defer h.mu.Unlock()

	sub := &subscriber{id: uuid.NewString(), project: want.Project, viewer: want.Viewer}
	after := h.sequence
	if want.After != "" {
		cursor, err := h.decodeReplayCursor(want.After)
		if err != nil || cursor.Generation != h.generation || cursor.Sequence > h.sequence {
			return nil, nil, ErrReplayUnavailable
		}
		after = cursor.Sequence
		if len(h.replay) > 0 && after+1 < h.replaySequence(h.replay[0]) {
			return nil, nil, ErrReplayUnavailable
		}
	}

	replay := make([]api.EventEnvelope, 0)
	for _, env := range h.replay {
		if h.replaySequence(env) > after && sub.receives(env) {
			replay = append(replay, env)
		}
	}
	capacity := SubscriberBufferSize
	if len(replay)+SubscriberBufferSize > capacity {
		capacity = len(replay) + SubscriberBufferSize
	}
	ch := make(chan api.EventEnvelope, capacity)
	for _, env := range replay {
		ch <- env
	}
	sub.ch = ch
	h.subscribers[sub.id] = sub
	return ch, h.unsubscribe(sub), nil
}

func (h *MemoryHub) replaySequence(env api.EventEnvelope) uint64 {
	cursor, err := h.decodeReplayCursor(env.Cursor)
	if err != nil {
		return 0
	}
	return cursor.Sequence
}

func (h *MemoryHub) unsubscribe(sub *subscriber) func() {
	return func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if existing, ok := h.subscribers[sub.id]; ok {
			delete(h.subscribers, sub.id)
			close(existing.ch)
		}
	}
}

// SubscriberCount reports how many subscribers currently hold the stream.
func (h *MemoryHub) SubscriberCount() int {
	if h == nil {
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subscribers)
}

var _ EventHub = (*MemoryHub)(nil)
var _ ReplayHub = (*MemoryHub)(nil)
