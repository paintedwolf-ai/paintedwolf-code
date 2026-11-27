package watch

import (
	"container/heap"
	"sync"
	"time"
)

// Clock supplies the current UTC time so tests advance deadlines deterministically.
type Clock interface {
	Now() time.Time
}

type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now().UTC() }

// DeadlineKind orders coinciding deadlines: handle expiry settles before timer ticks or wait timeouts.
type DeadlineKind int

const (
	KindWatchExpiry DeadlineKind = iota + 1
	KindTimerTick
	KindScheduleRun
	KindSamplerProbe
	KindWaitDeadline
)

type DeadlineKey struct {
	Kind DeadlineKind
	ID   string
}

type DeadlineEntry struct {
	Key   DeadlineKey
	DueAt time.Time
	seq   uint64
	index int
}

type deadlineSlice []*DeadlineEntry

func (h deadlineSlice) Len() int { return len(h) }

func (h deadlineSlice) Less(i, j int) bool {
	if !h[i].DueAt.Equal(h[j].DueAt) {
		return h[i].DueAt.Before(h[j].DueAt)
	}
	if h[i].Key.Kind != h[j].Key.Kind {
		return h[i].Key.Kind < h[j].Key.Kind
	}
	return h[i].seq < h[j].seq
}

func (h deadlineSlice) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index = i
	h[j].index = j
}

func (h *deadlineSlice) Push(x any) {
	entry := x.(*DeadlineEntry)
	entry.index = len(*h)
	*h = append(*h, entry)
}

func (h *deadlineSlice) Pop() any {
	old := *h
	n := len(old)
	entry := old[n-1]
	old[n-1] = nil
	entry.index = -1
	*h = old[:n-1]
	return entry
}

// DeadlineHeap maintains all due times in a single min-heap of absolute instants.
type DeadlineHeap struct {
	mu      sync.Mutex
	items   deadlineSlice
	byKey   map[DeadlineKey]*DeadlineEntry
	nextSeq uint64
}

func NewDeadlineHeap() *DeadlineHeap {
	return &DeadlineHeap{byKey: make(map[DeadlineKey]*DeadlineEntry)}
}

func (d *DeadlineHeap) Upsert(kind DeadlineKind, id string, dueAt time.Time) {
	if id == "" || dueAt.IsZero() {
		return
	}
	dueAt = dueAt.UTC()
	key := DeadlineKey{Kind: kind, ID: id}
	d.mu.Lock()
	defer d.mu.Unlock()
	if existing, ok := d.byKey[key]; ok {
		existing.DueAt = dueAt
		heap.Fix(&d.items, existing.index)
		return
	}
	d.nextSeq++
	entry := &DeadlineEntry{Key: key, DueAt: dueAt, seq: d.nextSeq}
	d.byKey[key] = entry
	heap.Push(&d.items, entry)
}

func (d *DeadlineHeap) Remove(kind DeadlineKind, id string) {
	key := DeadlineKey{Kind: kind, ID: id}
	d.mu.Lock()
	defer d.mu.Unlock()
	existing, ok := d.byKey[key]
	if !ok {
		return
	}
	delete(d.byKey, key)
	heap.Remove(&d.items, existing.index)
}

func (d *DeadlineHeap) Reset() {
	d.mu.Lock()
	d.items = nil
	d.byKey = make(map[DeadlineKey]*DeadlineEntry)
	d.nextSeq = 0
	d.mu.Unlock()
}

func (d *DeadlineHeap) Len() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.items)
}

func (d *DeadlineHeap) Peek() (DeadlineEntry, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.items) == 0 {
		return DeadlineEntry{}, false
	}
	return *d.items[0], true
}

// PopDue removes and returns all entries with DueAt <= now in priority order.
func (d *DeadlineHeap) PopDue(now time.Time) []DeadlineEntry {
	now = now.UTC()
	d.mu.Lock()
	defer d.mu.Unlock()
	var due []DeadlineEntry
	for len(d.items) > 0 && !d.items[0].DueAt.After(now) {
		entry := heap.Pop(&d.items).(*DeadlineEntry)
		delete(d.byKey, entry.Key)
		due = append(due, *entry)
	}
	return due
}
