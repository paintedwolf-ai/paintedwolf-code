package sourceview

import (
	"errors"
	"fmt"
	"slices"
)

// DiffOp indexes an unchanged, removed or added line in the original inputs.
type DiffOp struct {
	Tag      byte
	Old, New int
}

var ErrDiffTooLarge = errors.New("diff comparison exceeds its work ceiling")

// DiffLimitError reports the actual retained comparison capacity requested.
type DiffLimitError struct{ MaximumCells, ObservedCells int64 }

func (e *DiffLimitError) Error() string {
	return fmt.Sprintf("diff comparison capacity %d exceeds %d cells", e.ObservedCells, e.MaximumCells)
}
func (e *DiffLimitError) Unwrap() error { return ErrDiffTooLarge }

// DiffScript matches common edges directly and finds the middle's shortest
// edit path with Myers frontiers. maxCells bounds retained four-byte cells,
// including the output script and frontier headers; zero is unbounded.
func DiffScript(oldLines, newLines []string, equal func(string, string) bool, maxCells int64) ([]DiffOp, error) {
	if equal == nil {
		equal = func(a, b string) bool { return a == b }
	}
	prefix := 0
	for prefix < len(oldLines) && prefix < len(newLines) && equal(oldLines[prefix], newLines[prefix]) {
		prefix++
	}
	suffix := 0
	for suffix < len(oldLines)-prefix && suffix < len(newLines)-prefix && equal(oldLines[len(oldLines)-1-suffix], newLines[len(newLines)-1-suffix]) {
		suffix++
	}
	a, b := oldLines[prefix:len(oldLines)-suffix], newLines[prefix:len(newLines)-suffix]
	cells := int64(len(oldLines)+len(newLines)) * 6
	if maxCells > 0 && cells > maxCells {
		return nil, &DiffLimitError{maxCells, cells}
	}
	ops := make([]DiffOp, 0, len(oldLines)+len(newLines))
	for k := 0; k < suffix; k++ {
		ops = append(ops, DiffOp{' ', len(oldLines) - 1 - k, len(newLines) - 1 - k})
	}
	trace, err := diffFrontiers(a, b, equal, maxCells, cells)
	if err != nil {
		return nil, err
	}
	x, y := len(a), len(b)
	for d := len(trace) - 1; d > 0; d-- {
		k := x - y
		previous := trace[d-1]
		prevK := k - 1
		if k == -d || (k != d && frontierAt(previous, k-1) < frontierAt(previous, k+1)) {
			prevK = k + 1
		}
		prevX := int(frontierAt(previous, prevK))
		prevY := prevX - prevK
		for x > prevX && y > prevY {
			x--
			y--
			ops = append(ops, DiffOp{' ', prefix + x, prefix + y})
		}
		if x == prevX {
			y--
			ops = append(ops, DiffOp{'+', -1, prefix + y})
		} else {
			x--
			ops = append(ops, DiffOp{'-', prefix + x, -1})
		}
	}
	for x > 0 && y > 0 {
		x--
		y--
		ops = append(ops, DiffOp{' ', prefix + x, prefix + y})
	}
	for k := prefix - 1; k >= 0; k-- {
		ops = append(ops, DiffOp{' ', k, k})
	}
	slices.Reverse(ops)
	return ops, nil
}

func frontierAt(frontier []int32, k int) int32 {
	depth := (len(frontier) - 1) / 2
	if k < -depth || k > depth {
		return -1
	}
	return frontier[k+depth]
}

func diffFrontiers(a, b []string, equal func(string, string) bool, maximum, cells int64) ([][]int32, error) {
	var trace [][]int32
	for depth := 0; depth <= len(a)+len(b); depth++ {
		width := 2*depth + 1
		observed := cells + int64(width) + 12*int64(depth+1)
		if maximum > 0 && observed > maximum {
			return nil, &DiffLimitError{maximum, observed}
		}
		cells += int64(width)
		frontier := make([]int32, width)
		for k := -depth; k <= depth; k += 2 {
			x := 0
			if depth > 0 {
				previous := trace[depth-1]
				if k == -depth || (k != depth && frontierAt(previous, k-1) < frontierAt(previous, k+1)) {
					x = int(frontierAt(previous, k+1))
				} else {
					x = int(frontierAt(previous, k-1)) + 1
				}
			}
			y := x - k
			for x < len(a) && y < len(b) && equal(a[x], b[y]) {
				x++
				y++
			}
			frontier[k+depth] = int32(x)
			if x >= len(a) && y >= len(b) {
				return append(trace, frontier), nil
			}
		}
		trace = append(trace, frontier)
	}
	return trace, nil
}
