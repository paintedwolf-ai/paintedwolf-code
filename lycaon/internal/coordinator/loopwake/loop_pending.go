package loopwake

import (
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
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

func (l *LoopEngine) sessionPendingQueue(sessionID string) *sessionNudgeQueue {
	if v, ok := l.pendingQueues.Load(sessionID); ok {
		return v.(*sessionNudgeQueue)
	}
	q := &sessionNudgeQueue{}
	actual, _ := l.pendingQueues.LoadOrStore(sessionID, q)
	return actual.(*sessionNudgeQueue)
}

func (l *LoopEngine) sessionDeferredQueue(sessionID string) *deferredNudgeQueue {
	if v, ok := l.pendingWorkerQueues.Load(sessionID); ok {
		return v.(*deferredNudgeQueue)
	}
	q := &deferredNudgeQueue{}
	actual, _ := l.pendingWorkerQueues.LoadOrStore(sessionID, q)
	return actual.(*deferredNudgeQueue)
}

func (l *LoopEngine) enqueuePending(sessionID string, pending pendingLoopWake) {
	l.sessionPendingQueue(sessionID).push(pending)
}

func (l *LoopEngine) deferPromptWake(ctx context.Context, sessionID string, pending pendingLoopWake) {
	pending.postTurnDrain = true
	l.enqueuePending(sessionID, pending)
	// The busy owner may have released before this enqueue.
	l.schedulePendingDrain(context.WithoutCancel(ctx), sessionID, true)
}

func (l *LoopEngine) schedulePendingDrain(ctx context.Context, sessionID string, postTurn bool) {
	if _, draining := l.pendingDrain.Load(sessionID); draining {
		return
	}
	if l.PromptExecutionActive(sessionID) {
		return
	}
	if _, active := l.promptActive.Load(sessionID); active {
		return
	}
	if l.hostTurnBlocked(ctx, sessionID) {
		l.notifyLoopQuiescent(ctx, sessionID)
		return
	}
	if _, ready := l.waitWinner(sessionID); ready {
		l.breakSleep(ctx, sessionID, "wait resolved", false)
		l.runWaitResumeAsync(ctx, sessionID)
		return
	}
	if _, pending := l.sessionPendingQueue(sessionID).peek(); !pending {
		l.notifyLoopQuiescent(ctx, sessionID)
		return
	}
	l.spawnAsyncTurn(ctx, sessionID, func(ctx context.Context) {
		l.drainPending(ctx, sessionID, postTurn)
	})
}

// HasPendingLoopWakes includes undelivered results, queued wakes, and admission in flight.
func (l *LoopEngine) HasPendingLoopWakes(sessionID string) bool {
	if l == nil || strings.TrimSpace(sessionID) == "" {
		return false
	}
	if _, draining := l.pendingDrain.Load(sessionID); draining {
		return true
	}
	if _, ok := l.waitWinner(sessionID); ok {
		return true
	}
	if _, ok := l.sessionPendingQueue(sessionID).peek(); ok {
		return true
	}
	return l.sessionDeferredQueue(sessionID).nonEmpty()
}

func (l *LoopEngine) drainPending(ctx context.Context, sessionID string, postTurnDrain bool) {
	if _, draining := l.pendingDrain.LoadOrStore(sessionID, struct{}{}); draining {
		return
	}
	defer func() {
		l.pendingDrain.Delete(sessionID)
		l.schedulePendingDrain(ctx, sessionID, postTurnDrain)
	}()
	for {
		if l.hostTurnBlocked(ctx, sessionID) {
			return
		}
		// Worker-cycle deferrals are reconsidered only after outcome acknowledgement.
		if l.workerCycleIdle(ctx, sessionID, "") {
			l.flushDeferredNudges(ctx, sessionID)
		}
		q := l.sessionPendingQueue(sessionID)
		pending, ok := q.pop()
		if !ok {
			return
		}
		pending.postTurnDrain = pending.postTurnDrain || postTurnDrain
		if !l.runPromptSync(ctx, sessionID, pending) {
			return
		}
	}
}

func (l *LoopEngine) lastPromptObservedSeq(sessionID string) uint64 {
	if v, ok := l.promptObservedSeq.Load(sessionID); ok {
		seq, _ := v.(uint64)
		return seq
	}
	return 0
}

func (l *LoopEngine) kickStillQueued(sessionID string, wake anchor.ID) bool {
	deps := l.loopDeps()
	if deps.HasQueuedKick == nil {
		return false
	}
	kickID := anchor.InformRender(wake)
	return kickID != "" && deps.HasQueuedKick(sessionID, kickID)
}

// A failed execution waits for explicit input; queued facts remain available.
func (l *LoopEngine) hostTurnBlocked(ctx context.Context, sessionID string) bool {
	blocked := l.loopDeps().HostTurnBlocked
	return blocked != nil && blocked(ctx, sessionID)
}
