package paginate

// Slice returns a page from items using offset/limit pagination.
// total is len(items). When truncated, nextOffset points at the next page start.
func Slice[T any](items []T, offset, limit int) (page []T, total int, truncated bool, nextOffset *int) {
	total = len(items)
	if offset < 0 {
		offset = 0
	}
	if offset >= total {
		return nil, total, false, nil
	}
	end := total
	if limit > 0 && offset+limit < end {
		end = offset + limit
		truncated = true
		next := end
		nextOffset = &next
	}
	return items[offset:end], total, truncated, nextOffset
}
