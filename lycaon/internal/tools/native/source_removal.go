package native

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/sourceeffect"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/pkg/api"
)

func measureAgentRemoval(ctx context.Context, tc tools.ToolContext, target mutationTarget, mutation *agentMutation, display string) error {
	if _, ok := recoverableRemoval(tc, *mutation); !ok {
		return nil
	}
	canMoveAside, err := tc.Source.SourceMutations.CanMoveAside(ctx, target.Location)
	if err != nil {
		return err
	}
	if canMoveAside {
		mutation.RemovalSnapshot = nil
		mutation.Unrecoverable = false
		return nil
	}
	snapshot, err := tc.Source.SourceMutations.InspectRemoval(ctx, target.Location, mutation.Memory)
	if err != nil {
		return err
	}
	if err := verifyEntrySnapshot(ctx, target.Location, mutation.BeforeSHA256, mutation.IsDir); err != nil {
		return err
	}
	mutation.RemovalSnapshot, mutation.BeforeSize = &snapshot, snapshot.Bytes
	mutation.Unrecoverable = true
	mutation.MetadataChange, err = permanentRemovalNote(ctx, display, mutation)
	return err
}

func permanentRemovalNote(ctx context.Context, display string, mutation *agentMutation) (string, error) {
	snapshot := mutation.RemovalSnapshot
	return guidance.RenderGuidance(ctx, "utility/source-remove-permanent", map[string]any{"path": display, "size": formatDeleteByteSize(snapshot.Bytes), "files": snapshot.Files, "content_bytes": snapshot.Bytes, "recovery_bytes": snapshot.RecoveryBytes, "recovery_size": formatDeleteByteSize(snapshot.RecoveryBytes), "recovery_known": snapshot.RecoverySizeKnown})
}

// A failed recovery capability is not consent to delete permanently. Recheck
// the selected content, then use the same explicit recovery-loss review.
func reviewUnavailableRemoval(ctx context.Context, tc tools.ToolContext, target mutationTarget, mutation *agentMutation) error {
	before := mutation.RemovalSnapshot
	current, err := tc.Source.SourceMutations.InspectRemoval(ctx, target.Location, mutation.Memory)
	if err != nil {
		return err
	}
	if before != nil {
		if before.Identity != current.Identity || before.TreeSHA != current.TreeSHA || before.Bytes != current.Bytes || before.Files != current.Files || before.RecoveryBytes != current.RecoveryBytes || before.RecoverySizeKnown != current.RecoverySizeKnown {
			return projectsource.ErrSourceMutationDiverged
		}
	} else if err := verifyEntrySnapshot(ctx, target.Location, mutation.BeforeSHA256, mutation.IsDir); err != nil {
		return err
	}
	mutation.RemovalSnapshot, mutation.BeforeSize, mutation.Unrecoverable = &current, current.Bytes, true
	mutation.MetadataChange, err = permanentRemovalNote(ctx, target.Location.Rel, mutation)
	if err != nil {
		return err
	}
	return reviewAgentMutation(ctx, tc, *mutation, target.Location, fseffect.Location{}, true)
}

func removeAgentTree(ctx context.Context, tc tools.ToolContext, target mutationTarget, display string, bytes int64, memory *toolcontract.ProgramMemory, recheck func() ([]sourceeffect.ReviewedFile, error)) error {
	mutation := agentMutation{Memory: memory, Op: api.SourceChangeOpDelete, AbsPath: target.Abs, BeforeSize: bytes, IsDir: true, Recursive: true}
	if err := measureAgentRemoval(ctx, tc, target, &mutation, display); err != nil {
		return err
	}
	reportMutationIntent(tc, mutation, target.Location, fseffect.Location{}, api.AgentIntentOperationDelete)
	if err := reviewAgentMutation(ctx, tc, mutation, target.Location, fseffect.Location{}, mutation.Unrecoverable); err != nil {
		return err
	}
	// Content measurement does not grant per-entry write authority.
	if recheck != nil {
		reviewed, err := recheck()
		if err != nil {
			return err
		}
		mutation.ReviewedFiles = reviewed
	}
	return discardAgentEntry(ctx, tc, target, mutation)
}

// Child instruction changes use the same preimage review as scalar removal;
// the eventual tree move remains one journal and recovery operation.
func reviewAgentTreePolicyRemoval(ctx context.Context, tc tools.ToolContext, target projectpaths.Resolved, memory *toolcontract.ProgramMemory) (sourceeffect.ReviewedFile, error) {
	before, err := captureRemovedEntry(ctx, target.EffectLocation(), memory, true)
	if err != nil {
		return sourceeffect.ReviewedFile{}, err
	}
	if before.release != nil {
		defer before.release()
	}
	release, err := memory.ReserveProgramState(ctx, int64(16<<10)+6*int64(len(before.content))+8*int64(len(target.Abs)))
	if err != nil {
		return sourceeffect.ReviewedFile{}, err
	}
	defer release()
	working, err := memory.Reserve(ctx, 36<<10)
	if err != nil {
		return sourceeffect.ReviewedFile{}, err
	}
	defer working()
	mutation := agentMutation{Memory: memory, Op: api.SourceChangeOpDelete, AbsPath: target.Abs, Before: before.content, BeforeSHA256: before.sha256, BeforeSize: before.size, IsDir: before.isDir}
	if err := reviewAgentMutation(ctx, tc, mutation, target.EffectLocation(), fseffect.Location{}, false); err != nil {
		return sourceeffect.ReviewedFile{}, err
	}
	return sourceeffect.ReviewedFile{Rel: target.ScopeRel, SHA256: before.sha256}, nil
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
