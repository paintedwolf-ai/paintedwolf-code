package native

import (
	"context"
	"fmt"
	"os"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/pkg/api"
)

// LinkInput describes one symbolic link entry. Target is stored literally.
type LinkInput struct {
	Path, Target string
	Force        bool
	Destination  toolcontract.ExecutionRevision
	Memory       *toolcontract.ProgramMemory
}

// Link publishes one reviewed link through the existing source mutation door.
func (t *WriteTool) Link(ctx context.Context, in LinkInput, tc tools.ToolContext) error {
	if err := assertProfileWriteScope(ctx, t.Boundary, tc, in.Path, "write"); err != nil {
		return writeScopeReject(ctx, t.Boundary, in.Path, tc.ProfileID(), "write", err)
	}
	if err := beforeWorkerMutation(ctx, tc, in.Path); err != nil {
		return err
	}
	resolved, err := projectpaths.ResolveWrite(ctx, t.Boundary, tc, in.Path)
	if err != nil {
		return err
	}
	if err := projectpaths.AdmitLinkTarget(tc, resolved.Abs, in.Target); err != nil {
		return err
	}
	loc := resolved.EffectLocation()
	before, err := captureRemovedEntry(ctx, loc, in.Memory, true)
	if err != nil {
		return err
	}
	if before.release != nil {
		defer before.release()
	}
	if before.isDir {
		return os.ErrExist
	}
	if before.exists && !in.Force {
		return os.ErrExist
	}
	content := []byte(in.Target)
	op := api.SourceChangeOpCreate
	if before.exists {
		op = api.SourceChangeOpWrite
	}
	change := agentMutation{Memory: in.Memory, Op: op, AbsPath: resolved.Abs, IsLink: true,
		Before: before.content, BeforeSHA256: before.sha256, BeforeSize: before.size,
		After: content, AfterSHA256: textfile.SHA256(content), AfterSize: int64(len(content))}
	if err := reviewAgentMutation(ctx, tc, change, loc, fseffect.Location{}, false); err != nil {
		return err
	}
	if err := (ExecutionRevisions{Boundary: t.Boundary}).Verify(ctx, tc, in.Destination); err != nil {
		return err
	}
	// A regular file keeps its own identity and recoverable removal. The link
	// starts a new identity after that entry is set aside under the one review.
	if before.exists && !before.isLink {
		removed := agentMutation{Memory: in.Memory, Op: api.SourceChangeOpDelete, AbsPath: resolved.Abs,
			Before: before.content, BeforeSHA256: before.sha256, BeforeSize: before.size}
		if err := discardAgentEntry(ctx, tc, resolvedMutationTarget(resolved), removed); err != nil {
			return err
		}
		change.Op, change.Before, change.BeforeSHA256, change.BeforeSize = api.SourceChangeOpCreate, nil, "", 0
	}
	journal, err := prepareAgentMutation(ctx, tc, change, loc, fseffect.Location{}, false)
	if err != nil {
		return err
	}
	err = applyPreparedAgentMutation(ctx, tc, change, journal, func() error {
		return fseffect.Link(fseffect.LinkRequest{Location: loc, Target: in.Target, Replace: in.Force,
			BeforeCommit: func(_ fseffect.Target) error {
				if err := ctx.Err(); err != nil {
					return err
				}
				if err := projectpaths.AdmitLinkTarget(tc, resolved.Abs, in.Target); err != nil {
					return err
				}
				if change.Op == api.SourceChangeOpWrite || !before.exists {
					if err := (ExecutionRevisions{Boundary: t.Boundary}).Verify(ctx, tc, in.Destination); err != nil {
						return err
					}
				}
				current, err := captureRemovedEntry(ctx, loc, in.Memory, false)
				if err != nil {
					return err
				}
				if current.release != nil {
					defer current.release()
				}
				if current.exists != (change.Op == api.SourceChangeOpWrite) || current.sha256 != change.BeforeSHA256 || current.isDir || (current.exists && !current.isLink) {
					return fmt.Errorf("%w: link entry changed before publication", errMutationSnapshotMoved)
				}
				return nil
			},
		})
	})
	if err != nil {
		return err
	}
	afterSuccessfulMutation(ctx, tc, resolved.Abs)
	return nil
}
