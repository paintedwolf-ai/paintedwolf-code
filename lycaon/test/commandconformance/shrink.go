package commandconformance

import "context"

// ShrinkResult is minimal under ReductionCandidates when Minimal is true.
type ShrinkResult struct {
	Program GeneratedProgram
	Trials  int
	Minimal bool
}

// Shrink reaches a fixed point or exhausts the explicit trial budget. The caller
// confirms the original mismatch and decides whether a trial retains that defect.
func Shrink(ctx context.Context, original GeneratedProgram, budget int, retains func(context.Context, Case) (bool, error)) (ShrinkResult, error) {
	result := ShrinkResult{Program: original}
	for {
		reduced := false
		for _, trial := range result.Program.ReductionCandidates() {
			if err := ctx.Err(); err != nil {
				return result, err
			}
			if result.Trials >= budget {
				return result, nil
			}
			result.Trials++
			keep, err := retains(ctx, trial.Case())
			if err != nil {
				return result, err
			}
			if keep {
				result.Program = trial
				reduced = true
				break
			}
		}
		if !reduced {
			result.Minimal = true
			return result, nil
		}
	}
}
