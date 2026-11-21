package sourceledger

import (
	"context"
	"log/slog"
	"strings"

	"github.com/lycaon/lycaon/internal/sourcesnapshot"
)

// VerificationState binds evidence to admitted source content, not watcher activity.
// A moving or unavailable snapshot supplies no current passing evidence.
func VerificationState(ctx context.Context, source *Inventory, root string) (revision, rootDigest string) {
	if source == nil || source.snapshots == nil || strings.TrimSpace(root) == "" {
		return "", ""
	}
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
