package worker

import (
	"context"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/pkg/api"
)

// BlockedPrerequisite is a waiting job whose producer can no longer supply
// it. Reason is closed, rejected or failed.
type BlockedPrerequisite struct {
	ConsumerJobID string
	ProducerJobID string
	Reason        string
}

// BlockedPrerequisites tells a coordinator, once per waiting job, that a
// prerequisite it waits on can no longer be met. The job keeps waiting until
// the coordinator starts a replacement or withdraws it.
type BlockedPrerequisites struct {
	List func(ctx context.Context) ([]BlockedPrerequisite, error)
	Jobs interface {
		Get(string) (*api.WorkerTask, bool)
	}
	Notify func(ctx context.Context, parentID, consumerJobID, producerJobID, reason string)

	mu     sync.Mutex
	raised map[string]bool
}

// Check raises a notice for each newly blocked waiting job.
func (b *BlockedPrerequisites) Check(ctx context.Context) {
	if b == nil || b.List == nil || b.Notify == nil {
		return
	}
	blocked, err := b.List(ctx)
	if err != nil {
		workerPollerLog.Warn("list blocked prerequisites", "error", err)
		return
	}
	for _, p := range blocked {
		if !b.first(p.ConsumerJobID) {
			continue
		}
		task, ok := b.Jobs.Get(p.ConsumerJobID)
		if !ok || strings.TrimSpace(task.ParentSessionID) == "" {
			continue
		}
		b.Notify(ctx, task.ParentSessionID, p.ConsumerJobID, p.ProducerJobID, p.Reason)
	}
}

// first reports whether this process has not yet raised the job's notice.
func (b *BlockedPrerequisites) first(jobID string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.raised == nil {
		b.raised = map[string]bool{}
	}
	if b.raised[jobID] {
		return false
	}
	b.raised[jobID] = true
	return true
}
