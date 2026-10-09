package loopwake

import (
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"sort"
	"strings"
	"sync"
)

// pendingLoopWake preserves wake ordering across prompt execution.
type pendingLoopWake struct {
	wake            anchor.ID
	seq             uint64
	inform          anchor.ID
	informHandled   bool
	legID           string
	completingJobID string
	env             anchor.Envelope
	runID           string
	revision        int64
	postTurnDrain   bool
}

type sessionNudgeQueue struct {
	mu     sync.Mutex
	nudges []pendingLoopWake
}

func (q *sessionNudgeQueue) push(n pendingLoopWake) {
	if n.wake == "" {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	// Sequence identity deduplicates retries and preserves their position.
	index := sort.Search(len(q.nudges), func(i int) bool { return q.nudges[i].seq >= n.seq })
	if n.seq > 0 && index < len(q.nudges) && q.nudges[index].seq == n.seq {
		return
	}
	q.nudges = append(q.nudges, pendingLoopWake{})
	copy(q.nudges[index+1:], q.nudges[index:])
	q.nudges[index] = n
}

func (q *sessionNudgeQueue) pop() (pendingLoopWake, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.nudges) == 0 {
		return pendingLoopWake{}, false
	}
	n := q.nudges[0]
	q.nudges = q.nudges[1:]
	return n, true
}

func (q *sessionNudgeQueue) peek() (pendingLoopWake, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.nudges) == 0 {
		return pendingLoopWake{}, false
	}
	return q.nudges[0], true
}

type deferredNudgeQueue struct {
	mu    sync.Mutex
	items []pendingLoopWake
}

func (q *deferredNudgeQueue) push(d pendingLoopWake) {
	if d.wake == "" {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.items = append(q.items, d)
}

// removeBudgetRequestForJob drops a terminal job's deferred budget-request wake.
func (q *deferredNudgeQueue) removeBudgetRequestForJob(jobID string) bool {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	kept := q.items[:0:0]
	found := false
	for _, it := range q.items {
		if it.wake == anchor.WorkerBudgetRequested && strings.TrimSpace(it.legID) == jobID {
			found = true
			continue
		}
		kept = append(kept, it)
	}
	q.items = kept
	return found
}

func (q *deferredNudgeQueue) drain() []pendingLoopWake {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) == 0 {
		return nil
	}
	out := append([]pendingLoopWake(nil), q.items...)
	q.items = nil
	return out
}

func (q *deferredNudgeQueue) nonEmpty() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items) > 0
}

// A failed execution waits for explicit input; queued facts remain available.
