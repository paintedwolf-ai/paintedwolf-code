package textsearch

import (
	"bytes"
	"context"
)

// foldedCandidate charges one comparison for every rejected starting position.
// Windows keep cancellation bounded while byte search skips unrelated prefixes.
func (e literalEngine) foldedCandidate(ctx context.Context, record []byte, first byte, from, end int, work *int64) (int, error) {
	lower := asciiFold(first)
	for from < end {
		if err := ctx.Err(); err != nil {
			return -1, err
		}
		next := min(end, from+4096)
		chunk := record[from:next]
		at := bytes.IndexByte(chunk, lower)
		if lower >= 'a' && lower <= 'z' {
			upper := bytes.IndexByte(chunk, lower-('a'-'A'))
			if upper >= 0 && (at < 0 || upper < at) {
				at = upper
			}
		}
		skipped := len(chunk)
		if at >= 0 {
			skipped = at
		}
		if int64(skipped) > e.limits.WorkSteps-*work {
			*work = max(*work, e.limits.WorkSteps)
			return -1, &BudgetError{Resource: "regex_work_steps", Maximum: e.limits.WorkSteps, Observed: *work + 1}
		}
		*work += int64(skipped)
		if at >= 0 {
			return from + at, nil
		}
		from = next
	}
	return -1, nil
}
