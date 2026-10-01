package stream

import (
	"container/list"
	"context"
	"log/slog"
	"sync"

	"github.com/lycaon/lycaon/pkg/api"
)

type ProjectionStore interface {
	PatchLiveProjection(context.Context, string, string, string, []api.ToolCall) error
}

// State holds transient streams; the session command store settles durable output.
type State struct {
	store        ProjectionStore
	streamMu     sync.Mutex
	streamReplay *streamReplayCache
	activeStream activeStreamState
	hub          *liveStreamHub
}

func New(store ProjectionStore) *State {
	return &State{store: store, streamReplay: newStreamReplayCache(streamReplayCacheEntries), hub: newLiveStreamHub()}
}

func (m *State) Content(messageID string) (string, bool) {
	m.streamMu.Lock()
	defer m.streamMu.Unlock()
	entry, ok := m.streamReplay.get(messageID)
	return entry.content, ok
}

// Tokens returns the exact cached chunks emitted for a message.
func (m *State) Tokens(messageID string) ([]string, bool) {
	m.streamMu.Lock()
	defer m.streamMu.Unlock()
	entry, ok := m.streamReplay.get(messageID)
	if !ok || len(entry.tokens) == 0 {
		return nil, false
	}
	return append([]string(nil), entry.tokens...), true
}

// Durable transcripts cover replay beyond this cache.
const streamReplayCacheEntries = 64

type streamReplayEntry struct {
	content string
	tokens  []string
}

type streamReplayCache struct {
	entries  map[string]*list.Element
	order    *list.List // front is most recently touched
	capacity int
}

type streamReplayNode struct {
	messageID string
	entry     streamReplayEntry
}

func newStreamReplayCache(capacity int) *streamReplayCache {
	if capacity < 1 {
		capacity = 1
	}
	return &streamReplayCache{
		entries:  make(map[string]*list.Element, capacity),
		order:    list.New(),
		capacity: capacity,
	}
}

func (c *streamReplayCache) put(messageID string, entry streamReplayEntry) {
	if el, ok := c.entries[messageID]; ok {
		el.Value.(*streamReplayNode).entry = entry
		c.order.MoveToFront(el)
		return
	}
	c.entries[messageID] = c.order.PushFront(&streamReplayNode{messageID: messageID, entry: entry})
	for c.order.Len() > c.capacity {
		oldest := c.order.Back()
		if oldest == nil {
			return
		}
		c.order.Remove(oldest)
		delete(c.entries, oldest.Value.(*streamReplayNode).messageID)
	}
}

func (c *streamReplayCache) get(messageID string) (streamReplayEntry, bool) {
	el, ok := c.entries[messageID]
	if !ok {
		return streamReplayEntry{}, false
	}
	c.order.MoveToFront(el)
	return el.Value.(*streamReplayNode).entry, true
}

func (m *State) CacheLive(sessionID, messageID, content string, tokens []string, generatingTokens int) {
	m.SetActive(sessionID, messageID, generatingTokens)
	m.CacheReplay(messageID, content, tokens)
}

func (m *State) CacheReplay(messageID, content string, tokens []string) {
	entry := streamReplayEntry{content: content, tokens: append([]string(nil), tokens...)}
	m.streamMu.Lock()
	defer m.streamMu.Unlock()
	m.streamReplay.put(messageID, entry)
}

// Project coalesces live draft persist and session-stream fan-out.
func (m *State) Project(ctx context.Context, sessionID string, msg api.Message) error {
	if m == nil || msg.ID == "" {
		return nil
	}
	m.Schedule(ctx, sessionID, m.StampMessage(sessionID, msg))
	return nil
}

func (m *State) persistLiveProjection(ctx context.Context, sessionID string, msg api.Message) error {
	if m == nil || m.store == nil || msg.ID == "" {
		return nil
	}
	if err := m.store.PatchLiveProjection(ctx, sessionID, msg.ID, msg.Content, livePersistToolCalls(msg.ToolCalls)); err != nil {
		slog.DebugContext(ctx, "live projection persist", "session_id", sessionID, "message_id", msg.ID, "err", err)
		return err
	}
	return nil
}

// livePersistToolCalls maps an empty batch to nil so COALESCE leaves the column.
func livePersistToolCalls(calls []api.ToolCall) []api.ToolCall {
	if len(calls) == 0 {
		return nil
	}
	return calls
}
