// Package catalogtest prepares source inventories for tests.
package catalogtest

import (
	"context"
	"fmt"
	"time"

	"github.com/lycaon/lycaon/internal/sourcecatalog"
)

// AwaitIndex waits for discovery to settle.
func AwaitIndex(ctx context.Context, catalog *sourcecatalog.Catalog, projectID string, root sourcecatalog.Root) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		status, err := catalog.Trees.IndexStatus(ctx, projectID, root)
		if err != nil {
			return err
		}
		if status.Complete && !status.Refreshing && status.Error == "" {
			return nil
		}
		if status.State == sourcecatalog.StateFailed {
			return fmt.Errorf("prepare source inventory: %s", status.Error)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}
