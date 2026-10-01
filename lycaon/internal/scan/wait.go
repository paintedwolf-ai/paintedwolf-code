package scan

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/pkg/api"
)

// WaitForScanID blocks until scanID is terminal or ctx ends.
func WaitForScanID(ctx context.Context, store *SQLStore, scanID string, poll time.Duration) (*api.CodeScan, error) {
	if store == nil {
		return nil, fmt.Errorf("scan store not configured")
	}
	scanID = strings.TrimSpace(scanID)
	if scanID == "" {
		return nil, fmt.Errorf("scan_id required")
	}
	if poll <= 0 {
		poll = 400 * time.Millisecond
	}
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	seen := map[string]struct{}{scanID: {}}
	for {
		rec, err := store.Get(ctx, scanID)
		if err != nil {
			return nil, err
		}
		if rec != nil && rec.Status == api.CodeScanStatusSuperseded && strings.TrimSpace(rec.ReplacementScanID) != "" {
			scanID = strings.TrimSpace(rec.ReplacementScanID)
			if _, exists := seen[scanID]; exists {
				return rec, fmt.Errorf("scan replacement cycle at %s", scanID)
			}
			seen[scanID] = struct{}{}
			continue
		}
		if rec != nil && StatusTerminal(rec.Status) {
			return rec, nil
		}
		select {
		case <-ctx.Done():
			if rec != nil {
				return rec, ctx.Err()
			}
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

// StoreFromCoordinator returns the SQL store when c is a wired CoordinatorImpl.
func StoreFromCoordinator(c ScanCoordinator) *SQLStore {
	if impl, ok := c.(*CoordinatorImpl); ok && impl != nil {
		return impl.Store
	}
	return nil
}

// StatusTerminal reports whether a scan has stopped for good.
func StatusTerminal(status api.CodeScanStatus) bool {
	switch status {
	case api.CodeScanStatusComplete, api.CodeScanStatusFailed, api.CodeScanStatusTimedOut,
		api.CodeScanStatusCanceled, api.CodeScanStatusSuperseded:
		return true
	default:
		return false
	}
}
