package kick

import (
	"sync"

	"github.com/google/uuid"

	"github.com/lycaon/lycaon/internal/guidance"
)

// Policy feedback has its own acknowledged queue. Lifecycle-kick eviction cannot
// discard a fired rule, and a staged lease remains immutable during new arrivals.
type policyQueue struct {
	mu        sync.Mutex
	pending   []guidance.PolicyFeedback
	staged    []guidance.PolicyFeedback
	lease     uint64
	messageID string
}

func (k *KickEngine) policyQueue(sessionID string) *policyQueue {
	queue, _ := k.policyQueues.LoadOrStore(sessionID, &policyQueue{})
	return queue.(*policyQueue)
}

func (k *KickEngine) QueuePolicyFeedback(sessionID string, entries []guidance.PolicyFeedback) {
	q := k.policyQueue(sessionID)
	q.mu.Lock()
	defer q.mu.Unlock()
	// [OAR-EVAL-20] Preserve every occurrence and its evaluation order.
	// OAR counters and conditions own repetition; this delivery queue does not.
	for _, entry := range entries {
		q.pending = append(q.pending, entry.Clone())
	}
}

type PolicyFeedbackLease struct {
	ID       string
	Sequence uint64
	Entries  []guidance.PolicyFeedback
}

func (k *KickEngine) LeasePolicyFeedback(sessionID string) PolicyFeedbackLease {
	q := k.policyQueue(sessionID)
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.staged) == 0 && len(q.pending) > 0 {
		q.staged, q.pending = q.pending, nil
		q.lease = k.nextLease.Add(1)
		q.messageID = uuid.NewString()
	}
	out := make([]guidance.PolicyFeedback, len(q.staged))
	for i, entry := range q.staged {
		out[i] = entry.Clone()
	}
	return PolicyFeedbackLease{ID: q.messageID, Sequence: q.lease, Entries: out}
}

func (k *KickEngine) AckPolicyFeedback(sessionID string, lease uint64) {
	q := k.policyQueue(sessionID)
	q.mu.Lock()
	defer q.mu.Unlock()
	if lease != 0 && lease == q.lease {
		q.staged = nil
		q.lease = 0
		q.messageID = ""
	}
}

func (q *policyQueue) clear() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.pending, q.staged, q.lease, q.messageID = nil, nil, 0, ""
}
