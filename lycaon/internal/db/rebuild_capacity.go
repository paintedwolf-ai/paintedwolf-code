package db

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"syscall"
)

func constrainRebuildCopy(ctx context.Context, database *sql.DB, destination string, reserve uint64) (func() error, error) {
	available, known, err := rebuildAvailableBytes(filepath.Dir(destination))
	if err != nil || !known {
		return func() error { return nil }, err
	}
	if available < reserve {
		return nil, &InsufficientSpaceError{NeededBytes: reserve, AvailableBytes: available}
	}
	return setRebuildPageLimit(ctx, database, available-reserve)
}

// Bound actual growth instead of requiring space proportional to discarded
// source pages. Leave modest filesystem headroom; the limit is local to the
// unpublished copy and is removed before it becomes the live database.
func setRebuildPageLimit(ctx context.Context, database *sql.DB, available uint64) (func() error, error) {
	var original, pages, pageSize uint64
	for _, query := range []struct {
		statement string
		out       *uint64
	}{{`PRAGMA main.max_page_count`, &original}, {`PRAGMA main.page_count`, &pages}, {`PRAGMA main.page_size`, &pageSize}} {
		if err := database.QueryRowContext(ctx, query.statement).Scan(query.out); err != nil {
			return nil, err
		}
	}
	if pageSize == 0 {
		return nil, fmt.Errorf("invalid rebuild page size")
	}
	budget := available - min(available/16, uint64(64<<20))
	extra := budget / pageSize
	if extra == 0 {
		return nil, fmt.Errorf("no space for retained store copy: %w", syscall.ENOSPC)
	}
	limit := original
	if pages < original && extra < original-pages {
		limit = pages + extra
	}
	if _, err := database.ExecContext(ctx, fmt.Sprintf(`PRAGMA main.max_page_count=%d`, limit)); err != nil { // #nosec G701
		return nil, err
	}
	return func() error {
		_, err := database.ExecContext(context.WithoutCancel(ctx), fmt.Sprintf(`PRAGMA main.max_page_count=%d`, original))
		return err
	}, nil
}
