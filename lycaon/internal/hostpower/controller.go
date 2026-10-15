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
	// Provider names the operating-system protocol holding the assertion.
	Provider() string
}

type inhibitor interface {
	// Availability is nil when an assertion can be requested, otherwise the reason it cannot.
	Availability() error
	Acquire() (lease, error)
}

// ProviderReasonCode classifies why an inhibition protocol cannot be used.
type ProviderReasonCode string

const (
	// ReasonBusUnreachable is a bus the protocol lives on that cannot be reached.
	ReasonBusUnreachable ProviderReasonCode = "bus_unreachable"
	// ReasonNotServed is a protocol whose bus name has no owner.
	ReasonNotServed ProviderReasonCode = "not_served"
	// ReasonQueryFailed is a bus query that failed.
	ReasonQueryFailed ProviderReasonCode = "query_failed"
	// ReasonNoDescriptorPassing is a connection that cannot carry the lease descriptor.
	ReasonNoDescriptorPassing ProviderReasonCode = "no_descriptor_passing"
	// ReasonNoApplicationID is a provider that refuses callers it cannot
	// attribute to an application.
	ReasonNoApplicationID ProviderReasonCode = "no_application_id"
	// ReasonPlatform is a platform without an inhibition protocol.
	ReasonPlatform ProviderReasonCode = "platform_unsupported"
)

// ProviderReason is why one inhibition protocol cannot be used.
type ProviderReason struct {
	Provider string
	Code     ProviderReasonCode
	Reason   string
}

// UnavailableError reports that no inhibition protocol can be used, with each
// protocol's own reason.
type UnavailableError struct {
	Reasons []ProviderReason
}

func (e *UnavailableError) Error() string {
	if len(e.Reasons) == 0 {
		return "sleep inhibition is unavailable on this platform"
	}
	parts := make([]string, 0, len(e.Reasons))
	for _, reason := range e.Reasons {
		parts = append(parts, reason.Provider+": "+reason.Reason)
	}
	return "no sleep inhibition provider is available (" + strings.Join(parts, "; ") + ")"
}

// Status is the live state shown in Settings.
type Status struct {
	Enabled         bool
	Supported       bool
	Inhibiting      bool
	ActiveWorkCount int
	LastError       string
	// Provider is the protocol behind the held assertion; empty when none is held.
	Provider string
	// Unavailable is why no assertion can be requested; empty when Supported.
	Unavailable string
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
	unavailable := c.availabilityLocked()
	status := Status{
		Enabled:         c.enabled,
		Supported:       unavailable == nil,
		Inhibiting:      c.lease != nil,
		ActiveWorkCount: len(c.active),
		LastError:       c.lastError,
	}
	if unavailable != nil {
		status.Unavailable = unavailable.Error()
	}
	if c.lease != nil {
		status.Provider = c.lease.Provider()
	}
	return status
}

func (c *Controller) availabilityLocked() error {
	if c.inhibitor == nil {
		return &UnavailableError{}
	}
	return c.inhibitor.Availability()
}

func (c *Controller) reconcileLocked() {
	active := c.enabled && len(c.active) > 0 && c.inhibitor != nil
	want := active && c.inhibitor.Availability() == nil
	if !want {
		c.lastError = ""
		if c.lease != nil {
			lease := c.lease
			c.lease = nil
			if err := lease.Release(); err != nil {
				c.lastError = err.Error()
			}
		}
		if active {
			c.scheduleRetryLocked()
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
