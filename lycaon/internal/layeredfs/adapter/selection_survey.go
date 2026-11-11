package adapter

import (
	"context"
	"errors"
	"io"
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
	err := walkBounded(ctx, src, &cost.EntriesRead, func(path string, d fs.DirEntry) (bool, error) {
		if skipsSeed(d) {
			return false, nil
		}
		cost.Entries++
		if !root.clonable && d.Type().IsRegular() {
			info, err := d.Info()
			if err != nil {
				return false, err
			}
			cost.UncloneableBytes += max(info.Size(), 0)
		}
		if stopAtBudget && cost.Total(fact) > fact.Limit {
			return false, errSurveyStopped
		}
		return true, nil
	})
	if errors.Is(err, errSurveyStopped) {
		cost.Partial = true
		return nil
	}
	return err
}

// surveyBatch is how many entries one directory read returns, which bounds how far past its stop a
// walk reads.
const surveyBatch = 256

// errWalkDone ends a bounded walk that found what it was looking for.
var errWalkDone = errors.New("walk done")

// walkBounded visits every entry below root, reading each directory surveyBatch entries at a time and
// counting what it reads in read. visit reports whether to descend into a directory; an error ends the
// walk. What this user cannot read is left out, as every tree leaves it out.
func walkBounded(ctx context.Context, root string, read *int64, visit func(path string, d fs.DirEntry) (bool, error)) error {
	stack := []string{root}
	for len(stack) > 0 {
		dir := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		f, err := os.Open(dir)
		if err != nil {
			if dir != root && (errors.Is(err, fs.ErrPermission) || errors.Is(err, fs.ErrNotExist)) {
				continue
			}
			return err
		}
		for {
			if err := ctx.Err(); err != nil {
				_ = f.Close()
				return err
			}
			entries, readErr := f.ReadDir(surveyBatch)
			*read += int64(len(entries))
			for _, d := range entries {
				path := filepath.Join(dir, d.Name())
				descend, err := visit(path, d)
				if err != nil {
					_ = f.Close()
					return err
				}
				if descend && d.IsDir() {
					stack = append(stack, path)
				}
			}
			if readErr != nil {
				_ = f.Close()
				if errors.Is(readErr, io.EOF) || (dir != root && errors.Is(readErr, fs.ErrPermission)) {
					break
				}
				return readErr
			}
		}
	}
	return nil
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

// ProbeCloneAt answers clone availability by cloning one file of the root into dir, which reads no
// content. A root with no regular file has nothing that would need cloning.
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
	sample, _, err := FirstRegularFile(ctx, root, nil)
	return sample, err
}

// FirstRegularFile finds a regular file below root, reading directories in bounded batches, and
// reports how many entries it read. keep, when set, decides which entries the search considers.
func FirstRegularFile(ctx context.Context, root string, keep func(path string, d fs.DirEntry) bool) (string, int64, error) {
	var sample string
	var read int64
	err := walkBounded(ctx, root, &read, func(path string, d fs.DirEntry) (bool, error) {
		if skipsSeed(d) || (keep != nil && !keep(path, d)) {
			return false, nil
		}
		if d.Type().IsRegular() {
			sample = path
			return false, errWalkDone
		}
		return true, nil
	})
	if errors.Is(err, errWalkDone) || errors.Is(err, fs.ErrNotExist) {
		err = nil
	}
	return sample, read, err
}
