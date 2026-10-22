package native

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/guidance"
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
	mutation.Unrecoverable = snapshot.Bytes > tc.Source.SourceMutations.RecoveryQuotaBytes()
	if mutation.Unrecoverable {
		mutation.MetadataChange, err = guidance.RenderGuidance(ctx, "source-remove-permanent", map[string]any{"path": display, "size": formatDeleteByteSize(snapshot.Bytes), "files": snapshot.Files})
	}
	return err
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
