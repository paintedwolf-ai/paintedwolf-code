package worker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/pkg/api"
)

// ParentWorkerWaiter signals parents after terminal delivery.
type ParentWorkerWaiter struct {
	mu      sync.Mutex
	waiters map[string][]chan struct{}
}

// NewParentWorkerWaiter constructs an empty waiter registry.
func NewParentWorkerWaiter() *ParentWorkerWaiter {
	return &ParentWorkerWaiter{waiters: make(map[string][]chan struct{})}
}

// NotifyParent wakes waiters after terminal delivery.
func (w *ParentWorkerWaiter) NotifyParent(parentSessionID string) {
	if w == nil {
		return
	}
	parentSessionID = strings.TrimSpace(parentSessionID)
	if parentSessionID == "" {
		return
	}
	w.mu.Lock()
	chs := w.waiters[parentSessionID]
	delete(w.waiters, parentSessionID)
	w.mu.Unlock()
	for _, ch := range chs {
		close(ch)
	}
}

// AwaitParentSessionWorkers waits for active work and pending outcomes.
func AwaitParentSessionWorkers(
	ctx context.Context,
	q WorkerQueue,
	waiter *ParentWorkerWaiter,
	projectID, parentSessionID string,
	lim settings.SessionLimits,
) error {
	if q == nil {
		return nil
	}
	parentSessionID = strings.TrimSpace(parentSessionID)
	projectID = strings.TrimSpace(projectID)
	if parentSessionID == "" || projectID == "" {
		return nil
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, lim.AwaitParentWorkersTimeout())
		defer cancel()
	}
	for {
		// Register first so completion cannot race the active-job check.
		ch := registerParentWaiter(waiter, parentSessionID)
		active, err := parentSessionActiveJobs(ctx, q, projectID, parentSessionID)
		if err != nil {
			unregisterParentWaiter(waiter, parentSessionID, ch)
			return err
		}
		if len(active) == 0 {
			unregisterParentWaiter(waiter, parentSessionID, ch)
			return nil
		}
		select {
		case <-ctx.Done():
			unregisterParentWaiter(waiter, parentSessionID, ch)
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return fmt.Errorf("timed out waiting for worker jobs for session %s", parentSessionID)
			}
			return ctx.Err()
		case <-ch:
		}
	}
}

func registerParentWaiter(w *ParentWorkerWaiter, parentSessionID string) chan struct{} {
	ch := make(chan struct{})
	if w == nil {
		return ch
	}
	w.mu.Lock()
	w.waiters[parentSessionID] = append(w.waiters[parentSessionID], ch)
	w.mu.Unlock()
	return ch
}

func unregisterParentWaiter(w *ParentWorkerWaiter, parentSessionID string, ch chan struct{}) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	list := w.waiters[parentSessionID]
	out := list[:0]
	for _, existing := range list {
		if existing != ch {
			out = append(out, existing)
		}
	}
	if len(out) == 0 {
		delete(w.waiters, parentSessionID)
	} else {
		w.waiters[parentSessionID] = out
	}
}

func parentSessionActiveJobs(ctx context.Context, q WorkerQueue, projectID, parentSessionID string) ([]api.WorkerTask, error) {
	active, err := q.ListBySession(ctx, projectID, parentSessionID, api.WorkerStatusPending, api.WorkerStatusRunning, api.WorkerStatusWaiting)
	if err != nil {
		return nil, err
	}
	pending, err := q.ListPendingOutcomes(ctx)
	if err != nil {
		return nil, err
	}
	for _, task := range pending {
		if task.ProjectID == projectID && task.ParentSessionID == parentSessionID {
			active = append(active, task)
		}
	}
	return active, nil
}
