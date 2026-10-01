package sourceledger

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/sourcesnapshot"
)

// SnapshotSource supplies the source inventory's content-addressed store and
// how long one observation may wait for it.
type SnapshotSource interface {
	SnapshotStore() *sourcesnapshot.Store
	ObservationBudget() time.Duration
}

// Command observation bounds its wait for the shared snapshot I/O lane.
const commandObservationBudget = 5 * time.Second

// ObservationBudget is the longest one command or verification observation waits.
func (s *Store) ObservationBudget() time.Duration {
	if s == nil {
		return commandObservationBudget
	}
	return s.observationBudget
}

// VerificationState binds evidence to admitted source content, not watcher activity.
// A moving or unavailable snapshot supplies no current passing evidence.
func VerificationState(ctx context.Context, source Recorder, root string) (revision, rootDigest string) {
	provider, ok := source.(SnapshotSource)
	if !ok || provider.SnapshotStore() == nil || strings.TrimSpace(root) == "" {
		return "", ""
	}
	ctx, cancel := context.WithTimeout(ctx, provider.ObservationBudget())
	defer cancel()
	snapshot, err := provider.SnapshotStore().Ensure(ctx, sourcesnapshot.Request{
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
