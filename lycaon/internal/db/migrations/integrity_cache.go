package migrations

import (
	"context"
	"errors"
	"fmt"
)

// WithIntegrityCache runs validation on a held connection with a 64 MiB page
// cache. Restore the observed setting before any migration sorting resumes.
func WithIntegrityCache(ctx context.Context, database DBTX, validate func() error) (err error) {
	var original int64
	if err := database.QueryRowContext(ctx, "PRAGMA cache_size").Scan(&original); err != nil {
		return fmt.Errorf("inspect integrity cache: %w", err)
	}
	defer func() {
		_, restoreErr := database.ExecContext(context.WithoutCancel(ctx), fmt.Sprintf("PRAGMA cache_size=%d", original))
		err = errors.Join(err, restoreErr)
	}()
	if _, err := database.ExecContext(ctx, "PRAGMA cache_size=-65536"); err != nil {
		return fmt.Errorf("configure integrity cache: %w", err)
	}
	return validate()
}
