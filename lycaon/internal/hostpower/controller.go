// Package hostpower keeps the host available while authoritative work is active.
package hostpower

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/pkg/api"
)

const retryDelay = 5 * time.Second

type lease interface {
	Release() error
	Done() <-chan error
}

type inhibitor interface {
	Supported() bool
	Acquire() (lease, error)
}

// Status is the live state shown in Settings.
type Status struct {
	Enabled         bool
	Supported       bool
	Inhibiting      bool
	ActiveWorkCount int
	LastError       string
}

// Controller holds one assertion while work is active.
type Controller struct {
	mu             sync.Mutex
	inhibitor      inhibitor
	enabled        bool
	active         map[string]struct{}
	lease          lease
	lastError      string
	retryScheduled bool
	retryDelay     time.Duration
	closed         bool
	ctx            context.Context
	cancel         context.CancelFunc
}

// New creates the platform controller.
func New(enabled bool) *Controller {
	return newController(enabled, platformInhibitor{})
}

func newController(enabled bool, inhibitor inhibitor) *Controller {
	ctx, cancel := context.WithCancel(context.Background())
	return &Controller{
		inhibitor:  inhibitor,
		enabled:    enabled,
		active:     make(map[string]struct{}),
		retryDelay: retryDelay,
		ctx:        ctx,
		cancel:     cancel,
	}
}

// SetEnabled applies the device preference immediately.
func (c *Controller) SetEnabled(enabled bool) {
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.enabled = enabled
	c.reconcileLocked()
	c.mu.Unlock()
}

// SetActive applies one idempotent structured work edge.
func (c *Controller) SetActive(key string, active bool) {
	if c == nil || strings.TrimSpace(key) == "" {
		return
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	if active {
		c.active[key] = struct{}{}
	} else {
		delete(c.active, key)
	}
	c.reconcileLocked()
	c.mu.Unlock()
}

// ObserveActivity consumes host activity edges.
func (c *Controller) ObserveActivity(ev api.ActivityEvent) {
	c.setStructuredActive("activity:", ev.ActivityID, ev.Status == api.ActivityStatusActive)
}

// ObserveTurnClock consumes session-tree clock edges.
func (c *Controller) ObserveTurnClock(ev api.TurnClock) {
	c.setStructuredActive("turn:", ev.SessionID, ev.Running)
}

// ObserveDelivered consumes committed worker and scan edges.
func (c *Controller) ObserveDelivered(topic api.EventTopic, data json.RawMessage) error {
	switch topic {
	case api.EventTopicWorker:
		var ev api.WorkerEvent
		if err := json.Unmarshal(data, &ev); err != nil {
			return fmt.Errorf("decode worker activity: %w", err)
		}
		c.setStructuredActive("worker:", ev.WorkerID, ev.Status == api.WorkerStatusPending || ev.Status == api.WorkerStatusRunning)
	case api.EventTopicScan:
		var ev api.CodeScanEvent
		if err := json.Unmarshal(data, &ev); err != nil {
			return fmt.Errorf("decode scan activity: %w", err)
		}
		c.setStructuredActive("scan:", ev.ScanID, ev.Status == api.CodeScanStatusPending || ev.Status == api.CodeScanStatusRunning)
	default:
	}
	return nil
}

func (c *Controller) setStructuredActive(prefix, id string, active bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return
	}
	c.SetActive(prefix+id, active)
}

// Snapshot returns a coherent live status.
func (c *Controller) Snapshot() Status {
	if c == nil {
		return Status{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	supported := c.inhibitor != nil && c.inhibitor.Supported()
	return Status{
		Enabled:         c.enabled,
		Supported:       supported,
		Inhibiting:      c.lease != nil,
		ActiveWorkCount: len(c.active),
		LastError:       c.lastError,
	}
}

func (c *Controller) reconcileLocked() {
	want := c.enabled && len(c.active) > 0 && c.inhibitor != nil && c.inhibitor.Supported()
	if !want {
		c.lastError = ""
		if c.lease != nil {
			lease := c.lease
			c.lease = nil
			if err := lease.Release(); err != nil {
				c.lastError = err.Error()
			}
		}
		return
	}
	if c.lease != nil {
		return
	}
	lease, err := c.inhibitor.Acquire()
	if err != nil {
		c.lastError = err.Error()
		c.scheduleRetryLocked()
		return
	}
	if lease == nil {
		c.lastError = "idle-sleep assertion was not acquired"
		c.scheduleRetryLocked()
		return
	}
	c.lease = lease
	c.lastError = ""
	go c.watch(lease)
}

func (c *Controller) watch(lease lease) {
	err, ok := <-lease.Done()
	if !ok {
		err = nil
	}
	c.mu.Lock()
	if c.lease == lease {
		c.lease = nil
		if err != nil {
			c.lastError = err.Error()
		} else {
			c.lastError = "idle-sleep assertion exited unexpectedly"
		}
		c.scheduleRetryLocked()
	}
	c.mu.Unlock()
}

func (c *Controller) scheduleRetryLocked() {
	if c.retryScheduled || c.closed || !c.enabled || len(c.active) == 0 {
		return
	}
	c.retryScheduled = true
	go func() {
		timer := time.NewTimer(c.retryDelay)
		defer timer.Stop()
		select {
		case <-c.ctx.Done():
			return
		case <-timer.C:
		}
		c.mu.Lock()
		c.retryScheduled = false
		c.reconcileLocked()
		c.mu.Unlock()
	}()
}

// Close releases the assertion held by this controller.
func (c *Controller) Close() error {
	if c == nil {
		return nil
	}
	c.cancel()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	if c.lease == nil {
		return nil
	}
	lease := c.lease
	c.lease = nil
	return lease.Release()
}
