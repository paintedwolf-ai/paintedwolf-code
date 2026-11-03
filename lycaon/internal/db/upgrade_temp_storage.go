package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

func checkUpgradeTempStorage(ctx context.Context, database DBTX, scratch uint64) error {
	var configured string
	if err := database.QueryRowContext(ctx, "PRAGMA temp_store_directory").Scan(&configured); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	roots, err := upgradeTempDirectories(configured)
	if err != nil {
		return err
	}
	if len(roots) == 0 {
		return fmt.Errorf("SQLite temporary storage directory is unavailable")
	}
	required, err := storageSum(scratch, 64<<20)
	if err != nil {
		return err
	}
	// Charge scratch on the data volume even when a candidate is elsewhere:
	// this also covers multiple APFS volumes sharing one allocation pool.
	for _, root := range roots {
		available, known, err := rebuildAvailableBytes(root)
		if err != nil {
			return err
		}
		if !known {
			return fmt.Errorf("temporary storage capacity is unavailable")
		}
		if available < required {
			return &InsufficientSpaceError{NeededBytes: required, AvailableBytes: available}
		}
	}
	return nil
}
