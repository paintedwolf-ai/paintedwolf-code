package sourceledger

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/sourcesnapshot"
)

// Command observation bounds its wait for the shared snapshot I/O lane.
const commandObservationBudget = 5 * time.Second

// ObservationBudget is the longest one command or verification observation waits.
func (s *Inventory) ObservationBudget() time.Duration {
	if s == nil {
		return commandObservationBudget
	}
	return s.observationBudget
}

// VerificationState binds evidence to admitted source content, not watcher activity.
// A moving or unavailable snapshot supplies no current passing evidence.
func VerificationState(ctx context.Context, source *Inventory, root string) (revision, rootDigest string) {
	if source == nil || source.snapshots == nil || strings.TrimSpace(root) == "" {
		return "", ""
	}
	ctx, cancel := context.WithTimeout(ctx, source.observationBudget)
	defer cancel()
	snapshot, err := source.snapshots.Ensure(ctx, sourcesnapshot.Request{
		Roots: []sourcesnapshot.Root{{Path: root}},
	})
	if err != nil {
		slog.WarnContext(ctx, "Observe verification source", "error", err)
		return "", ""
	}
	if snapshot.Quality != sourcesnapshot.CaptureExact || snapshot.AdmissionMode == sourcesnapshot.AdmissionScopeBounded {
		return "", ""
	}
	return snapshot.MerkleSHA256, snapshot.RootsKey
}
