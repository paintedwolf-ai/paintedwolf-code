package bundleddriver

import (
	"context"
	"errors"
	"runtime"
	"syscall"

	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
)

// Batches bound process arguments, not repository coverage. Both supported
// analysis modes operate within individual files.
func scanOpengrepTargetBatches(ctx context.Context, targets []string, run func([]string) (*scanoutput.Result, error)) (*scanoutput.Result, error) {
	if len(targets) == 0 {
		return nil, errors.New("opengrep requires scan targets")
	}
	budget := 128 << 10
	if runtime.GOOS == "windows" {
		// Leave room for quoting, UTF-16 expansion, and the scanner options.
		budget = 8 << 10
	}
	var result scanoutput.Result
	var collect func([]string) error
	collect = func(batch []string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		part, err := run(batch)
		if errors.Is(err, syscall.E2BIG) && len(batch) > 1 {
			// An unusually large environment can reduce the available argument space.
			middle := len(batch) / 2
			if err := collect(batch[:middle]); err != nil {
				return err
			}
			return collect(batch[middle:])
		}
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		result.Categories = part.Categories
		result.ScannedPaths = append(result.ScannedPaths, part.ScannedPaths...)
		result.Findings = append(result.Findings, part.Findings...)
		result.FindingsCount += part.FindingsCount
		result.Warnings = append(result.Warnings, part.Warnings...)
		return nil
	}
	for start := 0; start < len(targets); {
		end, size := start, 0
		for end < len(targets) && (end == start || size+len(targets[end])+1 <= budget) {
			size += len(targets[end]) + 1
			end++
		}
		if err := collect(targets[start:end]); err != nil {
			return nil, err
		}
		start = end
	}
	return &result, nil
}
