package db

import (
	"context"
	"fmt"
)

const (
	// Each slice is one autocommit transaction, so it bounds the writer hold.
	vacuumSlicePages    = 128
	vacuumSlicesPerPass = 16
	// Removing a free page at the end of the file searches the freelist's trunk
	// chain from its head, so a slice's cost is pages times trunks. This budget
	// keeps 128-page slices until the chain is about 512 trunks long.
	vacuumSliceTrunkVisits = vacuumSlicePages * 512

	autoVacuumIncremental = 2
)

// VacuumPass reports what one pass observed. Page counts are read from the
// store before and after, never inferred from the pages requested.
type VacuumPass struct {
	AutoVacuumMode int64
	Slices         int64
	PagesRequested int64
	PagesReclaimed int64
	PageSize       int64
	PageCount      int64
	FreelistPages  int64
	// Progressed distinguishes a working pass from one that found nothing to do.
	// Foreground frees can grow the freelist while the file still shrinks.
	Progressed bool
}

func readPragmaInt(ctx context.Context, database Handle, pragma string) (int64, error) {
	var value int64
	if err := database.QueryRowContext(ctx, `PRAGMA `+pragma).Scan(&value); err != nil {
		return 0, fmt.Errorf("read %s: %w", pragma, err)
	}
	return value, nil
}

func runIncrementalVacuum(ctx context.Context, database Handle, minFreelistPages int64) (VacuumPass, error) {
	var pass VacuumPass
	var err error
	if pass.AutoVacuumMode, err = readPragmaInt(ctx, database, "auto_vacuum"); err != nil {
		return pass, err
	}
	if pass.PageSize, err = readPragmaInt(ctx, database, "page_size"); err != nil {
		return pass, err
	}
	if pass.PageCount, err = readPragmaInt(ctx, database, "page_count"); err != nil {
		return pass, err
	}
	if pass.FreelistPages, err = readPragmaInt(ctx, database, "freelist_count"); err != nil {
		return pass, err
	}
	if pass.AutoVacuumMode != autoVacuumIncremental {
		return pass, nil
	}
	store, _ := database.(*Store)
	for range vacuumSlicesPerPass {
		if pass.FreelistPages < minFreelistPages {
			return pass, nil
		}
		pages := vacuumPagesForSlice(pass.FreelistPages, pass.PageSize)
		if store != nil {
			if err := store.fitVacuumCache(ctx, pass.FreelistPages, pass.PageSize); err != nil {
				return pass, err
			}
		}
		if _, err := database.ExecContext(ctx, fmt.Sprintf(`PRAGMA incremental_vacuum(%d)`, pages)); err != nil {
			return pass, fmt.Errorf("incremental vacuum store: %w", err)
		}
		pass.Slices++
		pass.PagesRequested += pages
		pageCount, err := readPragmaInt(ctx, database, "page_count")
		if err != nil {
			return pass, err
		}
		freelist, err := readPragmaInt(ctx, database, "freelist_count")
		if err != nil {
			return pass, err
		}
		reclaimed := pass.PageCount - pageCount
		shrank := freelist < pass.FreelistPages
		pass.PageCount, pass.FreelistPages = pageCount, freelist
		if reclaimed <= 0 && !shrank {
			return pass, nil
		}
		pass.PagesReclaimed += max(reclaimed, 0)
		pass.Progressed = true
	}
	return pass, nil
}

func freelistTrunks(freelistPages, pageSize int64) int64 {
	// A trunk names at most usable/4 - 8 leaves and stays on the list itself.
	leaves := max(pageSize/4-8, 1)
	return freelistPages/(leaves+1) + 1
}

func vacuumPagesForSlice(freelistPages, pageSize int64) int64 {
	return min(max(vacuumSliceTrunkVisits/freelistTrunks(freelistPages, pageSize), 1), vacuumSlicePages)
}

const (
	// The store's writer keeps SQLite's default 2 MiB page cache outside a drain.
	defaultWriterCacheSize = -2000
	vacuumCacheSlackPages  = 256
	vacuumCacheStepPages   = 1024
	vacuumCacheMaxPages    = 16 * vacuumCacheStepPages
)

// A walk longer than the page cache evicts every trunk before its next visit,
// so the cache follows the chain, in steps, until the drain ends. Zero keeps the default.
func vacuumCachePages(freelistPages, pageSize int64) int64 {
	needed := freelistTrunks(freelistPages, pageSize) + vacuumCacheSlackPages
	if needed*pageSize <= -defaultWriterCacheSize*1024 {
		return 0
	}
	return min((needed/vacuumCacheStepPages+1)*vacuumCacheStepPages, vacuumCacheMaxPages)
}

func (s *Store) fitVacuumCache(ctx context.Context, freelistPages, pageSize int64) error {
	pages := vacuumCachePages(freelistPages, pageSize)
	if s.maintenanceState.vacuumCache.Swap(pages) == pages {
		return nil
	}
	size := int64(defaultWriterCacheSize)
	if pages > 0 {
		size = pages
	}
	if _, err := s.writer.ExecContext(ctx, fmt.Sprintf(`PRAGMA cache_size=%d`, size)); err != nil {
		return fmt.Errorf("size vacuum cache: %w", err)
	}
	return nil
}

// releaseVacuumCache returns the writer to its default cache after a drain.
func (s *Store) releaseVacuumCache(ctx context.Context) error {
	if s.maintenanceState.vacuumCache.Swap(0) == 0 {
		return nil
	}
	if _, err := s.writer.ExecContext(ctx, fmt.Sprintf(`PRAGMA cache_size=%d`, defaultWriterCacheSize)); err != nil {
		return fmt.Errorf("release vacuum cache: %w", err)
	}
	return nil
}
