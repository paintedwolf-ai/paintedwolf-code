package sourcecatalog

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// Maintenance changes storage bytes without renewing reader activity.
func checkpointTreeStore(ctx context.Context, file string, vacuumPages int) (err error) {
	if strings.HasSuffix(file, structuralFileSuffix) {
		return nil
	}
	lastUsed := lastTreeUse(file)
	defer func() { err = errors.Join(err, restoreTreeUse(file, lastUsed)) }()
	database, err := openTreeDB(ctx, file)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, database.Close()) }()
	if _, err := database.ExecContext(ctx, fmt.Sprintf("PRAGMA incremental_vacuum(%d)", vacuumPages)); err != nil {
		return err
	}
	_, err = database.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)")
	return err
}

func restoreTreeUse(file string, lastUsed time.Time) error {
	var errs []error
	for _, candidate := range append([]string{file}, treeSidecarPaths(file)...) {
		if err := os.Chtimes(candidate, lastUsed, lastUsed); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
