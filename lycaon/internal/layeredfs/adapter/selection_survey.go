package adapter

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/fileclone"
)

type rootCostFacts struct {
	grant    RootGrant
	clonable bool
}

func classifyRootClones(ctx context.Context, req ChooseRequest) ([]rootCostFacts, bool, error) {
	roots := make([]rootCostFacts, 0, len(req.Grants))
	anyCopyOnly := false
	for _, g := range req.Grants {
		clonable := false
		if req.CloneAvailable != nil {
			var err error
			clonable, err = req.CloneAvailable(ctx, g)
			if err != nil {
				return nil, false, err
			}
		}
		anyCopyOnly = anyCopyOnly || !clonable
		roots = append(roots, rootCostFacts{grant: g, clonable: clonable})
	}
	return roots, anyCopyOnly, nil
}

// countMaterializeCost uses index facts where a root has them and surveys the rest. With
// stopAtBudget the survey ends as soon as the running cost passes the budget, so an over-budget tree
// costs a bounded walk; bytes of a clonable root are never stat-ed, because they cost nothing.
func countMaterializeCost(ctx context.Context, roots []rootCostFacts, fact BudgetFact, stopAtBudget bool) (MaterializeCost, error) {
	cost := MaterializeCost{Source: CostFromIndex}
	for _, root := range roots {
		if e := root.grant.Estimate; e != nil {
			cost.Entries += e.EstimatedEntries
			if !root.clonable {
				cost.UncloneableBytes += e.EstimatedBytes
			}
			continue
		}
		cost.Source = CostFromSurvey
		if err := surveyRootCost(ctx, root, fact, stopAtBudget, &cost); err != nil {
			return MaterializeCost{}, err
		}
		if cost.Partial {
			return cost, nil
		}
	}
	return cost, nil
}

var errSurveyStopped = errors.New("survey reached the budget")

func surveyRootCost(ctx context.Context, root rootCostFacts, fact BudgetFact, stopAtBudget bool, cost *MaterializeCost) error {
	src := filepath.Clean(root.grant.SourcePath)
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path == src {
			return nil
		}
		if skipsSeed(d) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		cost.Entries++
		if !root.clonable && d.Type().IsRegular() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			cost.UncloneableBytes += max(info.Size(), 0)
		}
		if stopAtBudget && cost.Total(fact) > fact.Limit {
			return errSurveyStopped
		}
		return nil
	})
	if errors.Is(err, errSurveyStopped) {
		cost.Partial = true
		return nil
	}
	return err
}

// skipsSeed names what no materialized tree holds: source Git metadata and engine state.
func skipsSeed(d fs.DirEntry) bool {
	return d.Name() == ".git" || (d.IsDir() && d.Name() == ".lycaon")
}

// overrunCost keeps whichever count knows more: a stale estimate can read lower than what the copy
// already saw.
func overrunCost(overrun *BudgetOverrunError, counted MaterializeCost) MaterializeCost {
	if counted.Entries < overrun.Entries {
		counted.Entries = overrun.Entries
		counted.Source = CostFromCopy
	}
	if half := overrun.CopiedBytes / 2; counted.UncloneableBytes < half {
		counted.UncloneableBytes = half
		counted.Source = CostFromCopy
	}
	return counted
}

// ProbeCloneAt is the clone probe a placement answer stands behind: one file of the root cloned into
// dir, which reads no content. A root with no regular file has nothing that would need cloning.
func ProbeCloneAt(dir string) CloneAvailability {
	return func(ctx context.Context, grant RootGrant) (bool, error) {
		sample, err := firstRegularFile(ctx, grant.SourcePath)
		if err != nil || sample == "" {
			return sample == "", err
		}
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return false, err
		}
		return fileclone.SupportedFrom(ctx, sample, dir)
	}
}

func firstRegularFile(ctx context.Context, root string) (string, error) {
	var sample string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, fs.ErrNotExist) {
				return nil
			}
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path != root && skipsSeed(d) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() {
			sample = path
			return filepath.SkipAll
		}
		return nil
	})
	return sample, err
}
