package editordoc

import (
	"strings"
	"sync"
	"time"
)

// PresenceGrace preserves presence across brief stream interruptions.
const PresenceGrace = 5 * time.Second

// ClientLiveness binds presence to event stream lifetimes.
type ClientLiveness struct {
	grace   time.Duration
	release func(clientID string)

	mu      sync.Mutex
	streams map[string]int
	expiry  map[string]*presenceExpiry
}

type presenceExpiry struct {
	timer *time.Timer
}

func NewClientLiveness(grace time.Duration, release func(clientID string)) *ClientLiveness {
	return &ClientLiveness{
		grace:   grace,
		release: release,
		streams: make(map[string]int),
		expiry:  make(map[string]*presenceExpiry),
	}
}

// Connected registers one live stream and returns the disconnect for it.
func (c *ClientLiveness) Connected(clientID string) func() {
	clientID = strings.TrimSpace(clientID)
	if c == nil || clientID == "" {
		return func() {}
	}
	c.mu.Lock()
	if expiry := c.expiry[clientID]; expiry != nil {
		expiry.timer.Stop()
		delete(c.expiry, clientID)
	}
	c.streams[clientID]++
	c.mu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { c.disconnected(clientID) }) }
}

// StreamCount reports live streams for a client.
func (c *ClientLiveness) StreamCount(clientID string) int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.streams[strings.TrimSpace(clientID)]
}

// disconnected starts the grace once a client's last stream ends.
func (c *ClientLiveness) disconnected(clientID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if remaining := c.streams[clientID] - 1; remaining > 0 {
		c.streams[clientID] = remaining
		return
	}
	delete(c.streams, clientID)
	if c.release == nil {
		return
	}
	expiry := &presenceExpiry{}
	expiry.timer = time.AfterFunc(c.grace, func() { c.expire(clientID, expiry) })
	c.expiry[clientID] = expiry
}

// expire releases the client's presence unless it reconnected inside the grace.
func (c *ClientLiveness) expire(clientID string, expiry *presenceExpiry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.expiry[clientID] != expiry {
		return
	}
	delete(c.expiry, clientID)
	// Holding the lock orders removal before reconnect.
	c.release(clientID)
}
