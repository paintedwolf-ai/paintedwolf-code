package native

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/sourceeffect"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func renameAgentEntry(ctx context.Context, tctx tools.ToolContext, from, to mutationTarget, displacedBytes int64, memory *toolcontract.ProgramMemory, reviewDisplacedPolicy func() ([]sourceeffect.ReviewedFile, error)) error {
	identity, err := fspath.EntryIdentity(from.Abs)
	if err != nil {
		return err
	}
	after, err := captureAgentFileAt(from.Location, ctx, memory, true)
	if err != nil {
		return err
	}
	if after.release != nil {
		defer after.release()
	}
	before, err := captureRemovedEntry(ctx, to.Location, memory, true)
	if err != nil {
		return err
	}
	if before.release != nil {
		defer before.release()
	}
	beforeSize := before.size
	if before.isDir && displacedBytes > 0 {
		beforeSize = displacedBytes
	}
	mutation := agentMutation{Memory: memory, Op: api.SourceChangeOpRename, AbsPath: to.Abs, FromAbsPath: from.Abs,
		Before: before.content, BeforeSHA256: before.sha256, BeforeSize: beforeSize,
		After: after.content, AfterSHA256: after.sha256, AfterSize: after.size, IsDir: after.isDir}
	reportMutationIntent(tctx, mutation, to.Location, from.Location, api.AgentIntentOperationMove)
	if err := reviewAgentMutation(ctx, tctx, mutation, to.Location, from.Location, false); err != nil {
		return err
	}
	replace := false
	if before.exists && before.isDir == after.isDir {
		// Preserve the replaced entry when native recovery is available.
		displaced := agentMutation{Memory: memory, Op: api.SourceChangeOpDelete, AbsPath: to.Abs,
			Before: before.content, BeforeSHA256: before.sha256, BeforeSize: beforeSize, IsDir: before.isDir, Recursive: before.isDir}
		if reviewDisplacedPolicy != nil {
			reviewed, err := reviewDisplacedPolicy()
			if err != nil {
				return err
			}
			displaced.ReviewedFiles = reviewed
		}
		if _, ok := recoverableRemoval(tctx, displaced); ok {
			if err := measureAgentRemoval(ctx, tctx, to, &displaced, to.Location.Rel); err != nil {
				return err
			}
			if displaced.Unrecoverable {
				if err := reviewAgentMutation(ctx, tctx, displaced, to.Location, fseffect.Location{}, true); err != nil {
					return err
				}
			}
			if err := discardAgentEntry(ctx, tctx, to, displaced); err != nil {
				return fmt.Errorf("set aside replaced destination: %w", err)
			}
			mutation.Before, mutation.BeforeSHA256, mutation.BeforeSize = nil, "", 0
		} else if !before.isDir {
			replace = true
		} else {
			identity, idErr := fspath.EntryIdentity(to.Abs)
			if idErr != nil {
				return idErr
			}
			if err := checkAgentMutationPolicies(ctx, tctx, displaced); err != nil {
				return err
			}
			if err := fseffect.RemoveTreeGuarded(to.Location, identity); err != nil {
				return err
			}
		}
	}
	journal, err := prepareAgentMutation(ctx, tctx, mutation, to.Location, from.Location, false)
	if err != nil {
		return err
	}
	return applyPreparedAgentMutation(ctx, tctx, mutation, journal, func() error {
		if replace {
			return fseffect.ReplaceByRelocateGuarded(from.Location, to.Location, identity)
		}
		return fseffect.RelocateGuarded(from.Location, to.Location, identity)
	})
}
