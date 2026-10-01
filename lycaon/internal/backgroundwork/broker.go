// Package backgroundwork admits expensive host work through one priority-aware broker.
package backgroundwork

import (
	"context"
	"errors"
	"runtime"
	"sort"
	"sync"
	"time"
)

// Priority orders work from user-visible latency to speculative background work.
type Priority uint8

const (
	PriorityInteractive Priority = iota
	PriorityIntegrity
	PriorityProactive
)

// Resource is a separately bounded host bottleneck.
type Resource string

const (
	ResourceMetadata  Resource = "metadata"
	ResourceDirectory Resource = "directory"
	ResourceIO        Resource = "io"
	ResourceCPU       Resource = "cpu"
)

// ErrSuperseded means a newer epoch replaced queued work with the same key.
var ErrSuperseded = errors.New("background work superseded")

// ErrAcquireTimeout means the request's own wait bound elapsed before admission.
var ErrAcquireTimeout = errors.New("background work admission timed out")

// AgingInterval is how long a waiter must queue to gain one priority band.
// Under strict priority alone a steady stream of interactive work holds a
// proactive request forever.
const AgingInterval = 750 * time.Millisecond

// Limits bound one resource. Total is the host-wide ceiling; PerLane, when
// positive, additionally bounds each distinct Request.Lane — one traversal per
// tree without making separate trees queue for each other.
type Limits struct {
	Total   int
	PerLane int
	// InteractiveReserve keeps capacity available while long background jobs run.
	InteractiveReserve int
}

// Request describes one admission claim. Epoch zero disables supersession.
type Request struct {
	Key      string
	Epoch    uint64
	Priority Priority
	// Interests tracks the most urgent current consumer of shared work.
	Interests *PriorityGroup
	// Lane isolates per-lane capacity. Empty shares one default lane.
	Lane      string
	Resources []Resource
	// Units reserves weighted capacity for internal parallelism.
	Units map[Resource]int
	// MaxWait bounds this request's own queueing. Zero waits for ctx only.
	MaxWait time.Duration
}

type waiter struct {
	req        Request
	seq        uint64
	queued     time.Time
	ready      chan struct{}
	err        error
	granted    bool
	background bool
	deadline   *time.Timer
}

// effectivePriority ages a waiter one band per AgingInterval spent queueing.
func (w *waiter) effectivePriority(now time.Time) Priority {
	priority := w.req.Priority
	if w.req.Interests != nil {
		priority = w.req.Interests.Priority(priority)
	}
	bands := int(now.Sub(w.queued) / AgingInterval)
	if bands <= 0 {
		return priority
	}
	if bands >= int(priority) {
		return PriorityInteractive
	}
	return priority - Priority(bands) //nolint:gosec // G115 — bands < priority
}

// Broker coordinates metadata traversal, disk streaming, and CPU-heavy work.
type Broker struct {
	mu             sync.Mutex
	limits         map[Resource]Limits
	used           map[Resource]int
	backgroundUsed map[Resource]int
	laneUsed       map[Resource]map[string]int
	pending        []*waiter
	seq            uint64
	now            func() time.Time
	// aging is the timer that re-dispatches queued work as it ages. It runs
	// only while something is waiting.
	aging *time.Timer
}

// New constructs a broker. Missing and non-positive totals disable a resource.
func New(limits map[Resource]Limits) *Broker {
	copied := make(map[Resource]Limits, len(limits))
	for resource, limit := range limits {
		if limit.Total > 0 {
			limit.InteractiveReserve = max(0, min(limit.InteractiveReserve, limit.Total-1))
			copied[resource] = limit
		}
	}
	return &Broker{
		limits: copied, used: make(map[Resource]int), backgroundUsed: make(map[Resource]int),
		laneUsed: make(map[Resource]map[string]int), now: time.Now,
	}
}

var process = New(map[Resource]Limits{
	// One metadata traversal per source root, four across the host.
	ResourceMetadata:  {Total: 4, PerLane: 1},
	ResourceDirectory: {Total: 8, PerLane: 4},
	ResourceIO:        {Total: 1},
	ResourceCPU:       {Total: processCPUCapacity(), InteractiveReserve: 1},
})

func processCPUCapacity() int {
	capacity := runtime.NumCPU() / 2
	if capacity < 2 {
		return 2
	}
	if capacity > 8 {
		return 8
	}
	return capacity
}

// Process returns the host-wide admission broker.
func Process() *Broker { return process }

// Acquire waits for all requested resources and returns an idempotent release.
func (b *Broker) Acquire(ctx context.Context, req Request) (func(), error) {
	if b == nil {
		return func() {}, nil
	}
	req.Resources = normalizeResources(req.Resources)
	b.mu.Lock()
	b.seq++
	w := &waiter{req: req, seq: b.seq, queued: b.now(), ready: make(chan struct{})}
	if req.Key != "" && req.Epoch > 0 {
		for _, queued := range b.pending {
			if !queued.granted && queued.req.Key == req.Key && queued.req.Epoch > 0 && queued.req.Epoch < req.Epoch {
				queued.err = ErrSuperseded
				close(queued.ready)
			}
		}
	}
	b.pending = append(b.pending, w)
	if req.MaxWait > 0 {
		w.deadline = time.AfterFunc(req.MaxWait, func() { b.expire(w) })
	}
	b.dispatchLocked()
	b.mu.Unlock()

	select {
	case <-ctx.Done():
		b.mu.Lock()
		if !w.granted && w.err == nil {
			w.err = ctx.Err()
			b.removeLocked(w)
		}
		granted := w.granted
		b.mu.Unlock()
		if !granted {
			b.stopDeadline(w)
			return nil, ctx.Err()
		}
	case <-w.ready:
		if w.err != nil {
			b.stopDeadline(w)
			return nil, w.err
		}
	}
	b.stopDeadline(w)

	var once sync.Once
	return func() {
		once.Do(func() {
			b.mu.Lock()
			for _, resource := range req.Resources {
				units := b.requestUnits(req, resource)
				b.used[resource] -= units
				if w.background {
					b.backgroundUsed[resource] -= units
				}
				b.releaseLaneLocked(resource, req.Lane, units)
			}
			b.dispatchLocked()
			b.mu.Unlock()
		})
	}, nil
}

func (b *Broker) stopDeadline(w *waiter) {
	b.mu.Lock()
	timer := w.deadline
	w.deadline = nil
	b.mu.Unlock()
	if timer != nil {
		timer.Stop()
	}
}

// expire fails one waiter whose own MaxWait elapsed before admission.
func (b *Broker) expire(w *waiter) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if w.granted || w.err != nil {
		return
	}
	w.err = ErrAcquireTimeout
	b.removeLocked(w)
	close(w.ready)
}

func (b *Broker) dispatchLocked() {
	b.compactLocked()
	now := b.now()
	sort.SliceStable(b.pending, func(i, j int) bool {
		left, right := b.pending[i].effectivePriority(now), b.pending[j].effectivePriority(now)
		if left != right {
			return left < right
		}
		return b.pending[i].seq < b.pending[j].seq
	})
	for _, w := range b.pending {
		background := !requestIsInteractive(w.req)
		if w.granted || w.err != nil || !b.availableLocked(w.req, background) {
			continue
		}
		for _, resource := range w.req.Resources {
			units := b.requestUnits(w.req, resource)
			b.used[resource] += units
			if background {
				b.backgroundUsed[resource] += units
			}
			b.claimLaneLocked(resource, w.req.Lane, units)
		}
		w.granted = true
		w.background = background
		close(w.ready)
	}
	b.compactLocked()
	b.scheduleAgingLocked()
}

// scheduleAgingLocked runs a timer only while work is queued, so a waiter that
// outlives its band is re-sorted without waiting for the next release.
func (b *Broker) scheduleAgingLocked() {
	if len(b.pending) == 0 {
		if b.aging != nil {
			b.aging.Stop()
			b.aging = nil
		}
		return
	}
	if b.aging != nil {
		return
	}
	b.aging = time.AfterFunc(AgingInterval, func() {
		b.mu.Lock()
		b.aging = nil
		b.dispatchLocked()
		b.mu.Unlock()
	})
}

func (b *Broker) availableLocked(req Request, background bool) bool {
	for _, resource := range req.Resources {
		limit := b.limits[resource]
		units := b.requestUnits(req, resource)
		if limit.Total <= 0 || b.used[resource]+units > limit.Total {
			return false
		}
		if background && b.backgroundUsed[resource]+units > limit.Total-limit.InteractiveReserve {
			return false
		}
		if limit.PerLane > 0 && b.laneUsed[resource][req.Lane]+units > limit.PerLane {
			return false
		}
	}
	return true
}

func (b *Broker) claimLaneLocked(resource Resource, lane string, units int) {
	if b.limits[resource].PerLane <= 0 {
		return
	}
	lanes := b.laneUsed[resource]
	if lanes == nil {
		lanes = make(map[string]int)
		b.laneUsed[resource] = lanes
	}
	lanes[lane] += units
}

func (b *Broker) releaseLaneLocked(resource Resource, lane string, units int) {
	lanes := b.laneUsed[resource]
	if lanes == nil {
		return
	}
	lanes[lane] -= units
	if lanes[lane] <= 0 {
		delete(lanes, lane)
	}
}

func (b *Broker) requestUnits(req Request, resource Resource) int {
	units := req.Units[resource]
	if units <= 0 {
		units = 1
	}
	limit := b.limits[resource]
	capacity := limit.Total
	// Keep weights stable when a joined consumer later changes urgency.
	if req.Priority != PriorityInteractive {
		capacity -= limit.InteractiveReserve
	}
	if capacity > 0 && units > capacity {
		return capacity
	}
	return units
}

func requestIsInteractive(req Request) bool {
	priority := req.Priority
	if req.Interests != nil {
		priority = req.Interests.Priority(priority)
	}
	return priority == PriorityInteractive
}

func (b *Broker) compactLocked() {
	out := b.pending[:0]
	for _, w := range b.pending {
		if !w.granted && w.err == nil {
			out = append(out, w)
		}
	}
	b.pending = out
}

func (b *Broker) removeLocked(target *waiter) {
	for i, w := range b.pending {
		if w == target {
			b.pending = append(b.pending[:i], b.pending[i+1:]...)
			return
		}
	}
}

func normalizeResources(resources []Resource) []Resource {
	seen := make(map[Resource]struct{}, len(resources))
	out := make([]Resource, 0, len(resources))
	for _, resource := range resources {
		if resource == "" {
			continue
		}
		if _, ok := seen[resource]; ok {
			continue
		}
		seen[resource] = struct{}{}
		out = append(out, resource)
	}
	return out
}
