package events

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/lycaon/lycaon/pkg/api"
)

type messagePatchCoalescer struct {
	hub    EventHub
	mu     sync.Mutex
	closed bool
	wg     sync.WaitGroup
	// pending is keyed by project, session, and message.
	pending map[string]patchPending
	timers  map[string]*time.Timer
}

type patchPending struct {
	key PublishKey
	ev  api.MessageEvent
}

func newMessagePatchCoalescer(hub EventHub) *messagePatchCoalescer {
	return &messagePatchCoalescer{
		hub:     hub,
		pending: make(map[string]patchPending),
		timers:  make(map[string]*time.Timer),
	}
}

func messagePatchKey(key PublishKey, msgID string) string {
	return fmt.Sprintf("%s:%s:%s", key.Project, key.Session, msgID)
}

func (c *messagePatchCoalescer) schedule(ctx context.Context, key PublishKey, ev api.MessageEvent) {
	if c == nil || c.hub == nil || ev.Message.ID == "" {
		return
	}
	patchKey := messagePatchKey(key, ev.Message.ID)

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}

	c.pending[patchKey] = patchPending{key: key, ev: ev}
	if timer, ok := c.timers[patchKey]; ok {
		if timer.Stop() {
			c.wg.Done()
		}
	}
	// Deferred patches remain deliverable after the turn is canceled.
	flushCtx := context.WithoutCancel(ctx)
	c.wg.Add(1)
	c.timers[patchKey] = time.AfterFunc(DebounceMessagePatch, func() {
		defer c.wg.Done()
		c.flush(flushCtx, patchKey)
	})
}

func (c *messagePatchCoalescer) flush(ctx context.Context, patchKey string) {
	c.mu.Lock()
	entry, ok := c.pending[patchKey]
	delete(c.pending, patchKey)
	delete(c.timers, patchKey)
	c.mu.Unlock()
	if !ok {
		return
	}
	c.deliver(ctx, entry)
}

// flushSession publishes pending patches for sessionID now.
func (c *messagePatchCoalescer) flushSession(ctx context.Context, sessionID string) {
	if c == nil || sessionID == "" {
		return
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	var entries []patchPending
	for key, entry := range c.pending {
		if entry.key.Session != sessionID {
			continue
		}
		entries = append(entries, entry)
		if timer, ok := c.timers[key]; ok {
			if timer.Stop() {
				c.wg.Done()
			}
			delete(c.timers, key)
		}
		delete(c.pending, key)
	}
	c.wg.Add(1)
	c.mu.Unlock()
	defer c.wg.Done()

	for _, entry := range entries {
		c.deliver(ctx, entry)
	}
}

func (c *messagePatchCoalescer) deliver(ctx context.Context, entry patchPending) {
	logPublishFailure(ctx, "PublishMessagePatch", api.EventTopicMessage, c.hub.Publish(ctx, api.EventTopicMessage, entry.key, entry.ev))
}

func (c *messagePatchCoalescer) stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	for _, timer := range c.timers {
		if timer.Stop() {
			c.wg.Done()
		}
	}
	clear(c.timers)
	clear(c.pending)
}
