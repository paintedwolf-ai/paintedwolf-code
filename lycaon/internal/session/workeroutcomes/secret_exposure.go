package workeroutcomes

import (
	"context"
	"fmt"
	"strings"
)

// MergeWorkerSecretExposureIntoParent propagates a child's secret exposure.
func MergeWorkerSecretExposureIntoParent(ctx context.Context, sessionStore Sessions, transcriptStore Transcript, evidenceStore Evidence, parentID, childID string) error {
	if sessionStore == nil || transcriptStore == nil || evidenceStore == nil {
		return nil
	}
	parentID = strings.TrimSpace(parentID)
	childID = strings.TrimSpace(childID)
	if parentID == "" || childID == "" {
		return nil
	}
	exposed, err := evidenceStore.SessionSecretExposure(ctx, childID)
	if err != nil {
		return fmt.Errorf("read child secret-exposure state: %w", err)
	}
	if !exposed {
		return nil
	}
	return evidenceStore.SeedSecretExposure(ctx, parentID)
}
