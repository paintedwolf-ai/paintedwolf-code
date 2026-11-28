package workspace

import (
	"context"
	"strings"
	"sync"
)

// branchLifecycleEntry coordinates in-process branch preparation and
// reconciliation for a single job/view identity. Preparation holds are
// re-entrant so a lane owner and workspace manager can both guard the same
// preparation without blocking each other; reconciliation takes exclusive
// ownership and revalidates durable retention before settling or deleting
// either the branch tree or its metadata.
type branchLifecycleEntry struct {
	prepCount   int
	reconciling bool
	waiters     int
	changed     chan struct{}
}

var branchLifecycle = struct {
	mu      sync.Mutex
	entries map[string]*branchLifecycleEntry
}{entries: make(map[string]*branchLifecycleEntry)}

func (e *branchLifecycleEntry) notify() {
	if e.changed != nil {
		close(e.changed)
		e.changed = nil
	}
}

func (e *branchLifecycleEntry) waitChan() <-chan struct{} {
	if e.changed == nil {
		e.changed = make(chan struct{})
	}
	return e.changed
}

func cleanupBranchLifecycleEntry(jobID string, e *branchLifecycleEntry) {
	if e.prepCount == 0 && !e.reconciling && e.waiters == 0 {
		delete(branchLifecycle.entries, jobID)
	}
}

// AcquireBranchPreparation guards jobID (both its branch tree and metadata)
// against concurrent reconciliation from the first visible provision step
// through durable publication. Nested calls for the same jobID within a
// preparation flow succeed without blocking.
func AcquireBranchPreparation(ctx context.Context, jobID string) (func(), error) {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return func() {}, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	branchLifecycle.mu.Lock()
	for {
		entry := branchLifecycle.entries[jobID]
		if entry == nil {
			entry = &branchLifecycleEntry{}
			branchLifecycle.entries[jobID] = entry
		}
		if !entry.reconciling {
			entry.prepCount++
			branchLifecycle.mu.Unlock()
			var once sync.Once
			return func() {
				once.Do(func() {
					branchLifecycle.mu.Lock()
					entry.prepCount--
					entry.notify()
					cleanupBranchLifecycleEntry(jobID, entry)
					branchLifecycle.mu.Unlock()
				})
			}, nil
		}
		ch := entry.waitChan()
		entry.waiters++
		branchLifecycle.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			branchLifecycle.mu.Lock()
			entry.waiters--
			cleanupBranchLifecycleEntry(jobID, entry)
			branchLifecycle.mu.Unlock()
			return nil, ctx.Err()
		}
		branchLifecycle.mu.Lock()
		entry.waiters--
		cleanupBranchLifecycleEntry(jobID, entry)
	}
}

// acquireBranchReconcile waits for any in-flight preparation of jobID to finish
// and acquires exclusive reconciliation ownership for jobID.
func acquireBranchReconcile(ctx context.Context, jobID string) (func(), error) {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return func() {}, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	branchLifecycle.mu.Lock()
	for {
		entry := branchLifecycle.entries[jobID]
		if entry == nil {
			entry = &branchLifecycleEntry{}
			branchLifecycle.entries[jobID] = entry
		}
		if entry.prepCount == 0 && !entry.reconciling {
			entry.reconciling = true
			branchLifecycle.mu.Unlock()
			var once sync.Once
			return func() {
				once.Do(func() {
					branchLifecycle.mu.Lock()
					entry.reconciling = false
					entry.notify()
					cleanupBranchLifecycleEntry(jobID, entry)
					branchLifecycle.mu.Unlock()
				})
			}, nil
		}
		ch := entry.waitChan()
		entry.waiters++
		branchLifecycle.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			branchLifecycle.mu.Lock()
			entry.waiters--
			cleanupBranchLifecycleEntry(jobID, entry)
			branchLifecycle.mu.Unlock()
			return nil, ctx.Err()
		}
		branchLifecycle.mu.Lock()
		entry.waiters--
		cleanupBranchLifecycleEntry(jobID, entry)
	}
}
