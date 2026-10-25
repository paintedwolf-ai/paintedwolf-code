package db

import (
	"context"
	"fmt"
	"path/filepath"
)

const auditMaximumBytes = 256 << 20
const auditUnknownCapacityBytes = 16 << 20

func auditFrameBudget(ctx context.Context, store *Store) (uint64, error) {
	var pageSize uint64
	if err := store.reader.QueryRowContext(ctx, "PRAGMA page_size").Scan(&pageSize); err != nil {
		return 0, err
	}
	if pageSize < 512 || pageSize > 65536 {
		return 0, fmt.Errorf("invalid audit page size: %d", pageSize)
	}
	free, known, err := auditAvailableBytes(filepath.Dir(store.health.path))
	if err != nil {
		// Capacity is advisory; an unavailable filesystem statistic must not
		// prevent verification. Checkpoint failures still interrupt the attempt.
		known = false
	}
	return auditBudgetFrames(free, known, pageSize), nil
}

func auditBudgetFrames(free uint64, known bool, pageSize uint64) uint64 {
	budget := uint64(auditUnknownCapacityBytes)
	if known {
		budget = min(uint64(auditMaximumBytes), free/8)
	}
	return budget / (pageSize + 24)
}
