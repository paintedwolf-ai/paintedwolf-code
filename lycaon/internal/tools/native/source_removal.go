package native

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func measureAgentRemoval(ctx context.Context, tc tools.ToolContext, target mutationTarget, mutation *agentMutation, display string) error {
	if _, ok := recoverableRemoval(tc, *mutation); !ok {
		return nil
	}
	snapshot, err := tc.Source.SourceMutations.InspectRemoval(ctx, target.Location)
	if err != nil {
		return err
	}
	if err := verifyEntrySnapshot(target.Location, mutation.BeforeSHA256, mutation.IsDir); err != nil {
		return err
	}
	mutation.RemovalSnapshot, mutation.BeforeSize = &snapshot, snapshot.Bytes
	mutation.Unrecoverable = !snapshot.RecoveryAvailable || snapshot.RecoveryBytes > tc.Source.SourceMutations.RecoveryQuotaBytes()
	if mutation.Unrecoverable {
		mutation.MetadataChange, err = permanentRemovalNote(ctx, display, mutation)
	}
	return err
}

func permanentRemovalNote(ctx context.Context, display string, mutation *agentMutation) (string, error) {
	snapshot := mutation.RemovalSnapshot
	return guidance.RenderGuidance(ctx, "source-remove-permanent", map[string]any{"path": display, "size": formatDeleteByteSize(snapshot.Bytes), "files": snapshot.Files, "content_bytes": snapshot.Bytes, "recovery_bytes": snapshot.RecoveryBytes, "recovery_size": formatDeleteByteSize(snapshot.RecoveryBytes), "recovery_known": snapshot.RecoverySizeKnown})
}

// A failed recovery capability is not consent to delete permanently. Recheck
// the selected content, then use the same explicit recovery-loss review.
func reviewUnavailableRemoval(ctx context.Context, tc tools.ToolContext, target mutationTarget, mutation *agentMutation) error {
	before := mutation.RemovalSnapshot
	current, err := tc.Source.SourceMutations.InspectRemoval(ctx, target.Location)
	if err != nil {
		return err
	}
	if before == nil || before.Identity != current.Identity || before.TreeSHA != current.TreeSHA || before.Bytes != current.Bytes || before.Files != current.Files || before.RecoveryBytes != current.RecoveryBytes || before.RecoverySizeKnown != current.RecoverySizeKnown {
		return projectsource.ErrSourceMutationDiverged
	}
	mutation.RemovalSnapshot, mutation.Unrecoverable = &current, true
	mutation.MetadataChange, err = permanentRemovalNote(ctx, target.Location.Rel, mutation)
	if err != nil {
		return err
	}
	return reviewAgentMutation(ctx, tc, *mutation, target.Location, fseffect.Location{}, true)
}

func removeAgentTree(ctx context.Context, tc tools.ToolContext, target mutationTarget, display string, bytes int64, recheck func() error) error {
	mutation := agentMutation{Op: api.SourceChangeOpDelete, AbsPath: target.Abs, BeforeSize: bytes, IsDir: true, Recursive: true}
	if err := measureAgentRemoval(ctx, tc, target, &mutation, display); err != nil {
		return err
	}
	// Content measurement does not grant per-entry write authority.
	if recheck != nil {
		if err := recheck(); err != nil {
			return err
		}
	}
	reportMutationIntent(tc, mutation, target.Location, fseffect.Location{}, api.AgentIntentOperationDelete)
	if err := reviewAgentMutation(ctx, tc, mutation, target.Location, fseffect.Location{}, mutation.Unrecoverable); err != nil {
		return err
	}
	return discardAgentEntry(ctx, tc, target, mutation)
}

func formatDeleteByteSize(bytes int64) string {
	switch {
	case bytes >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(bytes)/float64(1<<30))
	case bytes >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(bytes)/float64(1<<20))
	case bytes >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(bytes)/float64(1<<10))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}
