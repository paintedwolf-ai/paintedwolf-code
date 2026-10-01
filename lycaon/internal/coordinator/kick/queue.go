package kick

import (
	"sync"
	"sync/atomic"
)

type kickQueueItem struct {
	lease  uint64
	kickID string
	// subject names the fact a per-subject kick reports (a job, leg, scan, or
	// process); kicks for different subjects queue separately.
	subject   string
	latestKey string
	nudge     string
	deferred  bool
	eager     bool
	eagerData map[string]string
	meta      kickMeta
	batchSeq  int
	hasSeq    bool
}

type sessionKickQueue struct {
	mu     sync.Mutex
	staged *kickQueueItem
	items  []kickQueueItem
}

// MaxQueuedGuidance bounds one session's queued kicks; the oldest is evicted.
const MaxQueuedGuidance = 32

func (q *sessionKickQueue) push(item kickQueueItem) {
	if item.kickID == "" {
		return
	}
	if !item.deferred && item.nudge == "" {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.pushLocked(item)
}

func (q *sessionKickQueue) pushLatest(item kickQueueItem, leases *atomic.Uint64) {
	if item.kickID == "" || item.latestKey == "" {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	item.lease = leases.Add(1)
	if q.staged != nil && q.staged.latestKey == item.latestKey {
		q.staged = &item
		return
	}
	kept := q.items[:0]
	for _, existing := range q.items {
		if existing.latestKey != item.latestKey {
			kept = append(kept, existing)
		}
	}
	q.items = kept
	q.pushLocked(item)
}

// covers reports whether a queued kick already carries other's guidance: the
// same kick about the same subject, or the same kick with no subject, which
// reports every subject at once.
func (item kickQueueItem) covers(other kickQueueItem) bool {
	return item.kickID == other.kickID && (item.subject == other.subject || item.subject == "")
}

func (q *sessionKickQueue) pushLocked(item kickQueueItem) {
	if q.staged != nil && q.staged.covers(item) {
		return
	}
	for _, existing := range q.items {
		if existing.covers(item) {
			return
		}
	}
	if len(q.items) >= MaxQueuedGuidance {
		q.items = q.items[1:]
	}
	q.items = append(q.items, item)
}

func (q *sessionKickQueue) peek() (kickQueueItem, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.staged != nil {
		return *q.staged, true
	}
	if len(q.items) == 0 {
		return kickQueueItem{}, false
	}
	return q.items[0], true
}

func (q *sessionKickQueue) has(kickID string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.staged != nil && q.staged.kickID == kickID {
		return true
	}
	for _, item := range q.items {
		if item.kickID == kickID {
			return true
		}
	}
	return false
}

func (q *sessionKickQueue) clear() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.staged = nil
	q.items = nil
}

func (q *sessionKickQueue) dropKickID(kickID string) {
	if kickID == "" {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.staged != nil && q.staged.kickID == kickID {
		q.staged = nil
	}
	if len(q.items) == 0 {
		return
	}
	kept := q.items[:0]
	for _, item := range q.items {
		if item.kickID == kickID {
			continue
		}
		kept = append(kept, item)
	}
	q.items = kept
}

func (q *sessionKickQueue) dropForBatchSeq(seq int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.staged != nil && q.staged.hasSeq && q.staged.batchSeq == seq {
		q.staged = nil
	}
	if len(q.items) == 0 {
		return
	}
	kept := q.items[:0]
	for _, item := range q.items {
		if item.hasSeq && item.batchSeq == seq {
			continue
		}
		kept = append(kept, item)
	}
	q.items = kept
}

func (q *sessionKickQueue) dropBeforeBatchSeq(liveSeq int) {
	if liveSeq <= 0 {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.staged != nil && q.staged.hasSeq && q.staged.batchSeq > 0 && q.staged.batchSeq < liveSeq {
		q.staged = nil
	}
	if len(q.items) == 0 {
		return
	}
	kept := q.items[:0]
	for _, item := range q.items {
		if item.hasSeq && item.batchSeq > 0 && item.batchSeq < liveSeq {
			continue
		}
		kept = append(kept, item)
	}
	q.items = kept
}

func (k *KickEngine) sessionKickQueue(sessionID string) *sessionKickQueue {
	if v, ok := k.kickQueues.Load(sessionID); ok {
		return v.(*sessionKickQueue)
	}
	q := &sessionKickQueue{}
	actual, _ := k.kickQueues.LoadOrStore(sessionID, q)
	return actual.(*sessionKickQueue)
}

// KickSkip reports queued guidance whose live obligation has cleared.
type KickSkip func(kickID, subject string) bool

func (q *sessionKickQueue) stage(skip KickSkip, leases *atomic.Uint64) string {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.staged != nil {
		if skip == nil || !skip(q.staged.kickID, q.staged.subject) {
			return q.staged.kickID
		}
		q.staged = nil
	}
	for len(q.items) > 0 {
		item := q.items[0]
		q.items = q.items[1:]
		if skip != nil && skip(item.kickID, item.subject) {
			continue
		}
		item.lease = leases.Add(1)
		q.staged = &item
		return item.kickID
	}
	return ""
}

// prune drops skipped kicks and lists the rest in delivery order.
func (q *sessionKickQueue) prune(skip KickSkip) []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	var ids []string
	if q.staged != nil {
		if skip != nil && skip(q.staged.kickID, q.staged.subject) {
			q.staged = nil
		} else {
			ids = append(ids, q.staged.kickID)
		}
	}
	kept := q.items[:0]
	for _, item := range q.items {
		if skip != nil && skip(item.kickID, item.subject) {
			continue
		}
		kept = append(kept, item)
		ids = append(ids, item.kickID)
	}
	q.items = kept
	return ids
}

func (q *sessionKickQueue) stageEager(item kickQueueItem, leases *atomic.Uint64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.staged != nil {
		q.pushLocked(item)
		return
	}
	item.lease = leases.Add(1)
	q.staged = &item
}

func (q *sessionKickQueue) stagedItem() *kickQueueItem {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.staged
}

func (q *sessionKickQueue) dropStaged(item *kickQueueItem) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.staged == item {
		q.staged = nil
	}
}

func (q *sessionKickQueue) ack(lease uint64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.staged != nil && q.staged.lease == lease {
		q.staged = nil
	}
}

// latest returns the kick held under latestKey, staged or queued.
func (q *sessionKickQueue) latest(latestKey string) (kickQueueItem, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.staged != nil && q.staged.latestKey == latestKey {
		return *q.staged, true
	}
	for _, item := range q.items {
		if item.latestKey == latestKey {
			return item, true
		}
	}
	return kickQueueItem{}, false
}

// dropLatest removes the kick held under latestKey when it is still the one
// leased; a newer replacement stays.
func (q *sessionKickQueue) dropLatest(latestKey string, lease uint64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.staged != nil && q.staged.latestKey == latestKey && q.staged.lease == lease {
		q.staged = nil
		return
	}
	kept := q.items[:0]
	for _, item := range q.items {
		if item.latestKey == latestKey && item.lease == lease {
			continue
		}
		kept = append(kept, item)
	}
	q.items = kept
}
