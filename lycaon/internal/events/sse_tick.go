package events

import (
	"sync"
	"time"
)

var (
	heartbeatInterval   = HeartbeatInterval
	heartbeatIntervalMu sync.RWMutex
)

// HeartbeatTicker returns a ticker for SSE idle pings.
func HeartbeatTicker() *time.Ticker {
	heartbeatIntervalMu.RLock()
	interval := heartbeatInterval
	heartbeatIntervalMu.RUnlock()
	return time.NewTicker(interval)
}

// SetHeartbeatIntervalForTests overrides the SSE ping interval; returns a restore func.
func SetHeartbeatIntervalForTests(d time.Duration) func() {
	heartbeatIntervalMu.Lock()
	prev := heartbeatInterval
	heartbeatInterval = d
	heartbeatIntervalMu.Unlock()
	return func() {
		heartbeatIntervalMu.Lock()
		heartbeatInterval = prev
		heartbeatIntervalMu.Unlock()
	}
}
