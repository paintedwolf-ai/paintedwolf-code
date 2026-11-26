package loopwake

import (
	"github.com/lycaon/lycaon/internal/actionoutcome"
	"github.com/lycaon/lycaon/pkg/api"
	"sort"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
)

// pendingLoopWake preserves wake ordering across prompt execution.
type pendingLoopWake struct {
	queue           *sessionNudgeQueue
	ticket          *actionoutcome.Ticket
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
	mu       sync.Mutex
	nudges   []pendingLoopWake
	deferred []pendingLoopWake
	closed   api.WakeReason
}

func (q *sessionNudgeQueue) push(n pendingLoopWake) {
	if n.wake == "" {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed != "" {
		n.ticket.Record(api.WakeStateDropped, q.closed, "", "")
		return
	}
	// Sequence identity deduplicates retries and preserves their position.
	index := sort.Search(len(q.nudges), func(i int) bool { return q.nudges[i].seq >= n.seq })
	if n.seq > 0 && index < len(q.nudges) && q.nudges[index].seq == n.seq {
		n.ticket.QueuedRetry()
		return
	}
	n.queue = q
	n.ticket.Record(api.WakeStateQueued, "", "", "")
	q.nudges = append(q.nudges, pendingLoopWake{})
	copy(q.nudges[index+1:], q.nudges[index:])
	q.nudges[index] = n
}

func (q *sessionNudgeQueue) close(reason api.WakeReason) {
	q.mu.Lock()
	q.closed = reason
	items := append(q.nudges, q.deferred...)
	q.nudges = nil
	q.deferred = nil
	q.mu.Unlock()
	for _, n := range items {
		n.ticket.Record(api.WakeStateDropped, reason, "", "")
	}
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

func (q *sessionNudgeQueue) deferWorker(d pendingLoopWake) {
	if d.wake == "" {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed != "" {
		d.ticket.Record(api.WakeStateDropped, q.closed, "", "")
		return
	}
	d.queue = q
	d.ticket.Record(api.WakeStateDeferred, api.WakeReasonWorkerCycle, "", "")
	q.deferred = append(q.deferred, d)
}

// removeRunningWorkerWakesForJob drops a terminal job's deferred wakes about it running.
func (q *sessionNudgeQueue) removeRunningWorkerWakesForJob(jobID string) bool {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	kept := q.deferred[:0:0]
	found := false
	for _, it := range q.deferred {
		if anchor.AboutRunningWorker(it.wake) && strings.TrimSpace(it.legID) == jobID {
			it.ticket.Record(api.WakeStateDropped, api.WakeReasonJobTerminal, "", "")
			found = true
			continue
		}
		kept = append(kept, it)
	}
	q.deferred = kept
	return found
}

func (q *sessionNudgeQueue) drainDeferred() []pendingLoopWake {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.deferred) == 0 {
		return nil
	}
	out := append([]pendingLoopWake(nil), q.deferred...)
	q.deferred = nil
	return out
}

func (q *sessionNudgeQueue) hasDeferred() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.deferred) > 0
}
