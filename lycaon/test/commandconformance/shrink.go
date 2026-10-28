package commandconformance

import (
	"context"
	"fmt"
)

// ShrinkResult is minimal under ReductionCandidates when Minimal is true.
type ShrinkResult struct {
	Program GeneratedProgram
	Trials  int
	Minimal bool
}

// Shrink compares ordered candidate batches, reaching a fixed point within the
// explicit trial budget. A batch lets reference runners amortize container setup.
func Shrink(ctx context.Context, original GeneratedProgram, budget int, compare func(context.Context, []Case) ([]bool, error)) (ShrinkResult, error) {
	result := ShrinkResult{Program: original}
	for {
		candidates := result.Program.ReductionCandidates()
		if len(candidates) == 0 {
			result.Minimal = true
			return result, nil
		}
		if err := ctx.Err(); err != nil {
			return result, err
		}
		remaining := budget - result.Trials
		if remaining <= 0 {
			return result, nil
		}
		if len(candidates) > remaining {
			candidates = candidates[:remaining]
		}
		cases := make([]Case, len(candidates))
		for i, candidate := range candidates {
			cases[i] = candidate.Case()
			cases[i].ID = fmt.Sprintf("%s-reduction-%d", cases[i].ID, result.Trials+i)
		}
		result.Trials += len(cases)
		kept, err := compare(ctx, cases)
		if err != nil {
			return result, err
		}
		if len(kept) != len(candidates) {
			return result, fmt.Errorf("comparison returned %d results for %d candidates", len(kept), len(candidates))
		}
		reduced := false
		for i, keep := range kept {
			if keep {
				result.Program = candidates[i]
				reduced = true
				break
			}
		}
		if !reduced {
			result.Minimal = len(candidates) == len(result.Program.ReductionCandidates())
			return result, nil
		}
	}
}
