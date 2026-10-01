package branchretention

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/lycaon/lycaon/internal/workspace"
)

// JobState is what the worker queue knows about one branch's job.
type JobState struct {
	// Sealed is true once the job has an overlay record, so its tree is
	// rebuildable. Only sealed trees are ever reclaimed.
	Sealed bool
}

// Deps are the queue facts and filesystem actions a sweep needs.
type Deps struct {
	BranchRoot string
	// Jobs returns the state of every job that still names a branch tree,
	// keyed by job id. A tree whose job is unknown is left to reconciliation.
	Jobs func(ctx context.Context) (map[string]JobState, error)
	// Evict reclaims one tree; it reports workspace.ErrBranchInUse when a
	// reader holds it, which the sweep treats as "not now".
	Evict func(ctx context.Context, branchRoot string) error
	// Now is the sweep clock.
	Now func() time.Time
}

// Report summarizes one sweep.
type Report struct {
	Trees          int
	AllocatedBytes int64
	EvictedIdle    int
	EvictedBudget  int
	// ReclaimedBytes is the allocated size of the trees that went.
	ReclaimedBytes int64
	Skipped        int
}

// Sweep reclaims idle sealed trees, then the least recently used sealed trees
// until the device total is within budget. Trees whose job is unsealed, in
// flight, or unknown are counted but never touched.
func Sweep(ctx context.Context, deps Deps, cfg Config) (Report, error) {
	cfg = cfg.normalized()
	if deps.Now == nil {
		deps.Now = time.Now
	}
	trees, err := workspace.ListJobTrees(ctx, deps.BranchRoot)
	if err != nil {
		return Report{}, fmt.Errorf("branch retention: inventory: %w", err)
	}
	jobs, err := deps.Jobs(ctx)
	if err != nil {
		return Report{}, fmt.Errorf("branch retention: job states: %w", err)
	}
	report := Report{}
	var candidates []workspace.JobTree
	for _, tree := range trees {
		if !tree.Present {
			continue
		}
		report.Trees++
		report.AllocatedBytes += tree.AllocatedBytes
		if state, ok := jobs[tree.JobID]; ok && state.Sealed {
			candidates = append(candidates, tree)
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].LastUsed.Before(candidates[j].LastUsed) })
	cutoff := deps.Now().Add(-cfg.IdleAge)
	total := report.AllocatedBytes
	for _, tree := range candidates {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		idle := !tree.LastUsed.After(cutoff)
		overBudget := total > cfg.BudgetBytes
		if !idle && !overBudget {
			break
		}
		if err := deps.Evict(ctx, tree.Root); err != nil {
			if errors.Is(err, workspace.ErrBranchInUse) {
				report.Skipped++
				continue
			}
			return report, fmt.Errorf("branch retention: evict %s: %w", tree.Root, err)
		}
		total -= tree.AllocatedBytes
		report.ReclaimedBytes += tree.AllocatedBytes
		reason := "idle"
		if idle {
			report.EvictedIdle++
		} else {
			reason = "budget"
			report.EvictedBudget++
		}
		slog.InfoContext(ctx, "reclaimed worker branch tree", "job_id", tree.JobID,
			"reason", reason, "allocated_bytes", tree.AllocatedBytes, "last_used", tree.LastUsed)
	}
	return report, nil
}

// Run sweeps once, then on every interval until ctx ends.
func Run(ctx context.Context, deps Deps, cfg Config) error {
	cfg = cfg.normalized()
	if _, err := Sweep(ctx, deps, cfg); err != nil {
		return err
	}
	ticker := time.NewTicker(cfg.SweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if _, err := Sweep(ctx, deps, cfg); err != nil {
				return err
			}
		}
	}
}

// ReclaimSealed reclaims every sealed tree regardless of age or budget. This
// is the Settings clear: in-flight and unsealed work is left alone, and a tree
// a reader holds is skipped.
func ReclaimSealed(ctx context.Context, deps Deps) (Report, error) {
	return Sweep(ctx, deps, Config{IdleAge: time.Nanosecond, BudgetBytes: 1, SweepInterval: time.Hour})
}

// Inventory reports whether any branch tree is on disk and their allocated size.
func Inventory(ctx context.Context, branchRoot string) (present bool, allocatedBytes int64, err error) {
	trees, err := workspace.ListJobTrees(ctx, branchRoot)
	if err != nil {
		return false, 0, err
	}
	for _, tree := range trees {
		if !tree.Present {
			continue
		}
		present = true
		allocatedBytes += tree.AllocatedBytes
	}
	return present, allocatedBytes, nil
}
