package survey

import "github.com/lycaon/lycaon/internal/tools/native/toolkit"

// PaginateLines returns a slice of lines for 1-based offset/limit and pagination metadata.
func PaginateLines(text string, offset, limit int) (page []string, total, endLine int, hasMore bool) {
	lines := toolkit.SplitLines(text)
	total = len(lines)
	if total == 0 || offset > total {
		return nil, total, 0, false
	}
	start := offset - 1
	end := start + limit
	if end > total {
		end = total
	}
	hasMore = end < total
	return lines[start:end], total, end, hasMore
}
