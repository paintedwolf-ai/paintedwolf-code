package worktrees

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/gitrepo"
)

type repoKey struct {
	projectID string
	repoID    string
}

type refreshWaiter struct {
	targetGen uint64
	done      chan refreshOutcome
}

type refreshOutcome struct {
	checkouts []Checkout
	err       error
}

type repoSlot struct {
	mu           sync.Mutex
	desiredGen   uint64
	completedGen uint64
	running      bool
	toplevel     string
	snapshot     []Checkout
	waiters      []*refreshWaiter
}

func (inv *Inventory) slotLocked(key repoKey) *repoSlot {
	slot, ok := inv.slots[key]
	if !ok {
		slot = &repoSlot{desiredGen: 1}
		inv.slots[key] = slot
	}
	return slot
}

func (inv *Inventory) slot(spec RepoSpec) (repoKey, *repoSlot, error) {
	key := repoKey{
		projectID: strings.TrimSpace(spec.ProjectID),
		repoID:    strings.TrimSpace(spec.RepoID),
	}
	if key.projectID == "" || key.repoID == "" {
		return repoKey{}, nil, fmt.Errorf("project_id and repo_id required")
	}
	top := canonicalPath(spec.Toplevel)
	if top == "" {
		return repoKey{}, nil, fmt.Errorf("repository toplevel required")
	}
	inv.mu.Lock()
	slot := inv.slotLocked(key)
	inv.mu.Unlock()
	slot.mu.Lock()
	if slot.toplevel != top {
		slot.toplevel = top
		if slot.completedGen > 0 && slot.desiredGen == slot.completedGen {
			slot.desiredGen++
		}
	}
	slot.mu.Unlock()
	return key, slot, nil
}

// NotifyChange marks a repository's inventory generation dirty without running Git eagerly.
// If a refresh is already running, one follow-up refresh runs automatically for the uncovered generation.
func (inv *Inventory) NotifyChange(projectID, repoID string) uint64 {
	key := repoKey{
		projectID: strings.TrimSpace(projectID),
		repoID:    strings.TrimSpace(repoID),
	}
	if key.projectID == "" || key.repoID == "" {
		return 0
	}
	inv.mu.Lock()
	slot := inv.slotLocked(key)
	inv.mu.Unlock()
	slot.mu.Lock()
	slot.desiredGen++
	gen := slot.desiredGen
	if slot.running {
		slot.mu.Unlock()
		return gen
	}
	slot.mu.Unlock()
	return gen
}

// ScheduleRefresh marks the repository dirty and starts a background reconciliation
// pass when none is running, returning immediately without blocking command delivery.
func (inv *Inventory) ScheduleRefresh(_ context.Context, spec RepoSpec) {
	key, slot, err := inv.slot(spec)
	if err != nil {
		return
	}
	slot.mu.Lock()
	slot.desiredGen++
	startWorker := !slot.running
	if startWorker {
		slot.running = true
	}
	slot.mu.Unlock()
	if startWorker {
		//nolint:contextcheck // Detached reconciliation outlives caller launch leases.
		go inv.runRefreshLoop(inv.baseCtx, key, slot)
	}
}

// ScheduleRepoRefreshForProject schedules background checkout reconciliation for projectID's repositories.
func (inv *Inventory) ScheduleRepoRefreshForProject(ctx context.Context, projectID string) {
	if inv == nil {
		return
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return
	}
	roots, err := db.New(inv.database).ListProjectRoots(ctx, projectID)
	if err != nil {
		return
	}
	seen := make(map[string]struct{}, len(roots))
	for _, root := range roots {
		repo, ok := gitrepo.Discover(root.Path)
		if !ok || repo.Root == "" {
			continue
		}
		top := canonicalPath(repo.Root)
		if top == "" {
			continue
		}
		if _, dup := seen[top]; dup {
			continue
		}
		seen[top] = struct{}{}
		if repoID := git.DeriveRepoID(top); repoID != "" {
			inv.ScheduleRefresh(ctx, RepoSpec{
				ProjectID: projectID,
				RepoID:    repoID,
				Toplevel:  top,
			})
		}
	}
}

// Read returns the inventory covering the latest requested generation, coalescing concurrent callers.
func (inv *Inventory) Read(ctx context.Context, spec RepoSpec) ([]Checkout, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key, slot, err := inv.slot(spec)
	if err != nil {
		return nil, err
	}
	slot.mu.Lock()
	if slot.completedGen >= slot.desiredGen && slot.completedGen > 0 {
		cached := append([]Checkout(nil), slot.snapshot...)
		slot.mu.Unlock()
		return cached, nil
	}
	return inv.awaitGenerationLocked(ctx, key, slot, slot.desiredGen)
}

// Refresh advances the generation when idle and coalesces concurrent refreshers on the active pass.
func (inv *Inventory) Refresh(ctx context.Context, spec RepoSpec) ([]Checkout, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key, slot, err := inv.slot(spec)
	if err != nil {
		return nil, err
	}
	slot.mu.Lock()
	if !slot.running && slot.completedGen >= slot.desiredGen {
		slot.desiredGen++
	}
	return inv.awaitGenerationLocked(ctx, key, slot, slot.desiredGen)
}

func (inv *Inventory) awaitGenerationLocked(ctx context.Context, key repoKey, slot *repoSlot, targetGen uint64) ([]Checkout, error) {
	waiter := &refreshWaiter{
		targetGen: targetGen,
		done:      make(chan refreshOutcome, 1),
	}
	slot.waiters = append(slot.waiters, waiter)
	startWorker := !slot.running
	if startWorker {
		slot.running = true
	}
	slot.mu.Unlock()

	if startWorker {
		//nolint:contextcheck // Detached reconciliation outlives caller launch leases.
		go inv.runRefreshLoop(inv.baseCtx, key, slot)
	}

	select {
	case out := <-waiter.done:
		if out.err != nil {
			return nil, out.err
		}
		return append([]Checkout(nil), out.checkouts...), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (inv *Inventory) runRefreshLoop(ctx context.Context, key repoKey, slot *repoSlot) {
	for {
		slot.mu.Lock()
		runGen := slot.desiredGen
		toplevel := slot.toplevel
		slot.mu.Unlock()

		checkouts, err := inv.reconcileRepository(ctx, RepoSpec{
			ProjectID: key.projectID,
			RepoID:    key.repoID,
			Toplevel:  toplevel,
		})

		slot.mu.Lock()
		if err == nil {
			slot.completedGen = runGen
			slot.snapshot = append([]Checkout(nil), checkouts...)
		}
		remaining := slot.waiters[:0]
		var ready []*refreshWaiter
		for _, w := range slot.waiters {
			if err != nil || w.targetGen <= runGen {
				ready = append(ready, w)
			} else {
				remaining = append(remaining, w)
			}
		}
		slot.waiters = remaining
		continueLoop := err == nil && (slot.desiredGen > slot.completedGen && len(slot.waiters) > 0 || slot.desiredGen > runGen)
		if !continueLoop {
			slot.running = false
		}
		slot.mu.Unlock()

		for _, w := range ready {
			w.done <- refreshOutcome{checkouts: checkouts, err: err}
		}
		if !continueLoop {
			return
		}
	}
}
