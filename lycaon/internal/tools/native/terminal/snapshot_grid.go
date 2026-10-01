package terminal

import (
	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/runeclamp"
)

type terminalSnapshotGrid struct {
	Cols       int      `json:"cols"`
	Rows       int      `json:"rows"`
	Lines      []string `json:"lines"`
	RowIndexes []int    `json:"row_indexes,omitempty"`
}

type terminalOmittedRowRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

type terminalScreenCoverage struct {
	RowsKept          int                       `json:"rows_kept"`
	RowsTotal         int                       `json:"rows_total"`
	OmittedRowRanges  []terminalOmittedRowRange `json:"omitted_row_ranges,omitempty"`
	CursorRowRetained bool                      `json:"cursor_row_retained"`
	Truncated         bool                      `json:"truncated"`
}

const terminalScreenTextBudget = 22 << 10

func boundedTerminalGrid(screen bgprocess.ScreenSnapshot) (terminalSnapshotGrid, terminalScreenCoverage) {
	grid := terminalSnapshotGrid{Cols: screen.Cols, Rows: screen.Rows}
	total := len(screen.Lines)
	coverage := terminalScreenCoverage{RowsTotal: total}
	if total == 0 {
		return grid, coverage
	}

	lineBytes := 0
	for _, line := range screen.Lines {
		lineBytes += len(line)
	}
	indexes := allTerminalRowIndexes(total)
	if lineBytes > terminalScreenTextBudget {
		indexes = boundedTerminalRowIndexes(total, screen.CursorRow)
		coverage.Truncated = true
		coverage.OmittedRowRanges = terminalOmittedRanges(total, indexes)
	}

	remaining := terminalScreenTextBudget
	for _, index := range indexes {
		if remaining <= 0 {
			coverage.Truncated = true
			break
		}
		line := runeclamp.ClampBytesMiddle(screen.Lines[index], remaining)
		if len(line) < len(screen.Lines[index]) {
			coverage.Truncated = true
		}
		grid.Lines = append(grid.Lines, line)
		grid.RowIndexes = append(grid.RowIndexes, index)
		remaining -= len(line)
	}
	if len(grid.RowIndexes) < len(indexes) {
		coverage.OmittedRowRanges = terminalOmittedRanges(total, grid.RowIndexes)
	}
	coverage.RowsKept = len(grid.Lines)
	for _, index := range grid.RowIndexes {
		if index == screen.CursorRow {
			coverage.CursorRowRetained = true
			break
		}
	}
	return grid, coverage
}

func allTerminalRowIndexes(total int) []int {
	indexes := make([]int, total)
	for i := range total {
		indexes[i] = i
	}
	return indexes
}

func boundedTerminalRowIndexes(total, cursorRow int) []int {
	const maxRows = 64
	if total <= maxRows {
		return allTerminalRowIndexes(total)
	}
	head := maxRows / 2
	tail := maxRows - head
	indexes := make([]int, 0, maxRows+1)
	for i := 0; i < head; i++ {
		indexes = append(indexes, i)
	}
	if cursorRow >= head && cursorRow < total-tail {
		indexes = append(indexes, cursorRow)
	}
	for i := total - tail; i < total; i++ {
		indexes = append(indexes, i)
	}
	return dedupeSortedTerminalRows(indexes)
}

func dedupeSortedTerminalRows(rows []int) []int {
	if len(rows) < 2 {
		return rows
	}
	out := rows[:0]
	last := -1
	for _, row := range rows {
		if row == last {
			continue
		}
		out = append(out, row)
		last = row
	}
	return out
}

func terminalOmittedRanges(total int, kept []int) []terminalOmittedRowRange {
	seen := make(map[int]struct{}, len(kept))
	for _, row := range kept {
		seen[row] = struct{}{}
	}
	ranges := make([]terminalOmittedRowRange, 0, 2)
	start := -1
	for row := 0; row < total; row++ {
		_, isKept := seen[row]
		if !isKept && start < 0 {
			start = row
		}
		if (isKept || row == total-1) && start >= 0 {
			end := row - 1
			if row == total-1 && !isKept {
				end = row
			}
			ranges = append(ranges, terminalOmittedRowRange{Start: start, End: end})
			start = -1
		}
	}
	return ranges
}
