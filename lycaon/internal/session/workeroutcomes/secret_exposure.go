package workeroutcomes

import (
	"context"
	"fmt"
	"strings"
)

// MergeWorkerSecretExposureIntoParent propagates a child's secret exposure.
func MergeWorkerSecretExposureIntoParent(ctx context.Context, store SummaryStore, parentID, childID string) error {
	if store == nil {
		return nil
	}
	parentID = strings.TrimSpace(parentID)
	childID = strings.TrimSpace(childID)
	if parentID == "" || childID == "" {
		return nil
	}
	exposed, err := store.SessionSecretExposure(ctx, childID)
	if err != nil {
		return fmt.Errorf("read child secret-exposure state: %w", err)
	}
	if !exposed {
		return nil
	}
	return store.SeedSecretExposure(ctx, parentID)
}
