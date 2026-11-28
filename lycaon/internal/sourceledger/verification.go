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
	return verificationState(ctx, source, root, false)
}

// SettledVerificationState flushes the root's watcher barrier before capturing
// source content, for authoritative gate evaluation. An unsettled wait fails
// closed by returning no current revision.
func SettledVerificationState(ctx context.Context, source *Inventory, root string) (revision, rootDigest string) {
	return verificationState(ctx, source, root, true)
}

func verificationState(ctx context.Context, source *Inventory, root string, settle bool) (revision, rootDigest string) {
	if source == nil || source.snapshots == nil || strings.TrimSpace(root) == "" {
		return "", ""
	}
	snapshot, err := source.snapshots.Ensure(ctx, sourcesnapshot.Request{
		Roots:  []sourcesnapshot.Root{{Path: root}},
		Settle: settle,
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
