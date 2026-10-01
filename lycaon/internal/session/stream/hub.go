// Package stream maintains transient session output, replay, and coalesced live projection.
package stream

import (
	"context"
	"reflect"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/pkg/api"
)

// Frame replaces content or ends a subscription.
type Frame struct {
	Content   string
	ToolCalls []api.ToolCall
	Done      bool
}

type liveStreamHub struct {
	mu         sync.Mutex
	flushMu    sync.Mutex
	subs       map[string]map[*liveStreamSubscriber]struct{}
	sessionMsg map[string]string

	pending map[string]livePending
	timers  map[string]*time.Timer
	latest  map[string]Frame
}

type liveStreamSubscriber struct {
	frames chan Frame
	closed bool
	last   Frame
}

func (s *liveStreamSubscriber) close() {
	if s == nil || s.closed {
		return
	}
	s.closed = true
	close(s.frames)
}

func (s *liveStreamSubscriber) deliver(frame Frame) {
	if s == nil || s.closed {
		return
	}
	if frame.Done {
		// Replace queued snapshots with the final state.
		drained := false
		for {
			select {
			case <-s.frames:
				drained = true
				continue
			default:
			}
			break
		}
		if (frame.Content != "" || len(frame.ToolCalls) > 0) && (drained || !sameLiveStreamSnapshot(s.last, frame)) {
			frame.Done = false
			s.frames <- frame
			s.last = frame
		}
		s.frames <- Frame{Done: true}
		return
	}
	select {
	case s.frames <- frame:
		s.last = frame
	default:
		// The next frame is another full snapshot.
	}
}

func sameLiveStreamSnapshot(a, b Frame) bool {
	if a.Content != b.Content || len(a.ToolCalls) != len(b.ToolCalls) {
		return false
	}
	for i := range a.ToolCalls {
		if !reflect.DeepEqual(a.ToolCalls[i], b.ToolCalls[i]) {
			return false
		}
	}
	return true
}

type livePending struct {
	sessionID string
	msg       api.Message
}

func newLiveStreamHub() *liveStreamHub {
	return &liveStreamHub{
		subs:       make(map[string]map[*liveStreamSubscriber]struct{}),
		sessionMsg: make(map[string]string),
		pending:    make(map[string]livePending),
		timers:     make(map[string]*time.Timer),
		latest:     make(map[string]Frame),
	}
}

// Subscribe starts with the latest published snapshot.
func (m *State) Subscribe(messageID string) (<-chan Frame, func()) {
	hub := m.hub
	if hub == nil || messageID == "" {
		ch := make(chan Frame)
		close(ch)
		return ch, func() {}
	}
	sub := &liveStreamSubscriber{frames: make(chan Frame, 8)}
	hub.mu.Lock()
	if hub.subs[messageID] == nil {
		hub.subs[messageID] = make(map[*liveStreamSubscriber]struct{})
	}
	hub.subs[messageID][sub] = struct{}{}
	if latest, ok := hub.latest[messageID]; ok {
		sub.deliver(latest)
	}
	hub.mu.Unlock()

	var unsubscribe sync.Once
	unsub := func() {
		unsubscribe.Do(func() {
			hub.mu.Lock()
			defer hub.mu.Unlock()
			if set, ok := hub.subs[messageID]; ok {
				delete(set, sub)
				if len(set) == 0 {
					delete(hub.subs, messageID)
				}
			}
			sub.close()
		})
	}
	return sub.frames, unsub
}

// Schedule coalesces persistence and session-stream delivery.
func (m *State) Schedule(ctx context.Context, sessionID string, msg api.Message) {
	hub := m.hub
	if hub == nil || msg.ID == "" {
		_ = m.persistLiveProjection(ctx, sessionID, msg)
		return
	}
	hub.mu.Lock()
	defer hub.mu.Unlock()
	hub.sessionMsg[sessionID] = msg.ID
	hub.pending[msg.ID] = livePending{sessionID: sessionID, msg: msg}
	if timer, ok := hub.timers[msg.ID]; ok {
		timer.Stop()
	}
	msgID := msg.ID
	// Persist the final draft even if the turn ends before the timer fires.
	flushCtx := context.WithoutCancel(ctx)
	hub.timers[msgID] = time.AfterFunc(events.DebounceLiveProjection, func() {
		hub.flush(flushCtx, msgID, m.persistLiveProjection)
	})
}

// Flush persists pending drafts and delivers their stream frames.
func (m *State) Flush(ctx context.Context, sessionID string) {
	hub := m.hub
	if hub == nil || sessionID == "" {
		return
	}
	hub.mu.Lock()
	var ids []string
	for id, p := range hub.pending {
		if p.sessionID == sessionID {
			ids = append(ids, id)
		}
	}
	hub.mu.Unlock()
	for _, id := range ids {
		hub.flush(ctx, id, m.persistLiveProjection)
	}
}

func (h *liveStreamHub) flush(ctx context.Context, messageID string, persist func(context.Context, string, api.Message) error) {
	h.flushMu.Lock()
	defer h.flushMu.Unlock()
	h.mu.Lock()
	p, ok := h.pending[messageID]
	delete(h.pending, messageID)
	if t, ok := h.timers[messageID]; ok {
		t.Stop()
		delete(h.timers, messageID)
	}
	h.mu.Unlock()
	if !ok {
		return
	}
	if persist != nil {
		_ = persist(ctx, p.sessionID, p.msg)
	}
	h.publish(p.msg)
}

func (h *liveStreamHub) publish(msg api.Message) {
	h.mu.Lock()
	defer h.mu.Unlock()
	frame := Frame{
		Content:   msg.Content,
		ToolCalls: append([]api.ToolCall(nil), msg.ToolCalls...),
	}
	h.latest[msg.ID] = frame
	h.deliver(msg.ID, frame)
}

func (h *liveStreamHub) deliver(messageID string, frame Frame) {
	set := h.subs[messageID]
	if len(set) == 0 {
		return
	}
	for sub := range set {
		sub.deliver(frame)
	}
}

func (m *State) signalDone(sessionID, activeMessageID string) {
	hub := m.hub
	if hub == nil {
		return
	}
	hub.flushMu.Lock()
	defer hub.flushMu.Unlock()
	hub.mu.Lock()
	msgID := hub.sessionMsg[sessionID]
	delete(hub.sessionMsg, sessionID)
	if msgID == "" {
		msgID = activeMessageID
	}
	// Prevent a delayed flush after settlement.
	if msgID != "" {
		delete(hub.pending, msgID)
		if t, ok := hub.timers[msgID]; ok {
			t.Stop()
			delete(hub.timers, msgID)
		}
	}
	if msgID == "" {
		hub.mu.Unlock()
		return
	}
	terminal := hub.latest[msgID]
	delete(hub.latest, msgID)
	terminal.Done = true
	hub.deliver(msgID, terminal)
	hub.mu.Unlock()
}
