package project

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fspath"
)

func (s *SourceMutationService) applySourceMove(ctx context.Context, row *sourceMutationRow) error {
	plan := &row.Plan
	if plan.CrossVolume && plan.HoldStarted {
		if !sourceMutationPathExists(plan.HoldAbs) && !plan.MoveCleanupStarted && sourceMutationPathExists(plan.FromAbs) {
			if err := requireSourceIdentity(plan.FromAbs, plan.EntryIdentity); err != nil {
				return err
			}
			if err := requireSourceFingerprint(ctx, plan.FromAbs, plan.TreeSHA); err != nil {
				return err
			}
			heldRel, err := sourceRelativePath(plan.RootPath, plan.HoldAbs)
			if err != nil {
				return err
			}
			if err := s.startSourceEffect(ctx, row); err != nil {
				return err
			}
			if err := fseffect.RenameGuarded(plan.RootPath, plan.FromPath, heldRel, plan.EntryIdentity); err != nil {
				return err
			}
		}
		return s.publishSourceMove(ctx, row)
	}
	if !sourceMutationPathExists(plan.FromAbs) && sourceMutationPathExists(plan.ToAbs) {
		return verifySourceMutationApplied(ctx, plan)
	}
	if err := requireSourceIdentity(plan.FromAbs, plan.EntryIdentity); err != nil {
		return err
	}
	if sourceMutationPathExists(plan.ToAbs) {
		return ErrSourceExists
	}
	parent := filepath.Dir(filepath.FromSlash(plan.ToPath))
	if parent != "." {
		if err := fseffect.MkdirAll(fseffect.Location{Root: plan.RootPath, Rel: parent}, sourceCreateDirMode); err != nil {
			return err
		}
	}
	same, err := s.sameFilesystem(plan.FromAbs, filepath.Dir(plan.ToAbs))
	if err != nil {
		return err
	}
	if same && !plan.CrossVolume {
		if err := s.startSourceEffect(ctx, row); err != nil {
			return err
		}
		return applySourceRename(plan)
	}
	plan.CrossVolume = true
	if err := s.prepareSourceCopy(ctx, row); err != nil {
		return err
	}
	plan.DestinationIdentity, err = fspath.EntryIdentity(filepath.Join(plan.StageAbs, "entry"))
	if err != nil {
		return err
	}
	plan.HoldAbs = filepath.Join(filepath.Dir(plan.FromAbs), ".paintedwolf-move-"+row.ID)
	if sourceMutationPathExists(plan.HoldAbs) {
		return ErrSourceExists
	}
	if err := s.startSourceEffect(ctx, row); err != nil {
		return err
	}
	plan.HoldStarted = true
	if err := s.update(ctx, row); err != nil {
		return err
	}
	heldRel, err := sourceRelativePath(plan.RootPath, plan.HoldAbs)
	if err != nil {
		return err
	}
	if err := fseffect.RenameGuarded(plan.RootPath, plan.FromPath, heldRel, plan.EntryIdentity); err != nil {
		return err
	}
	return s.publishSourceMove(ctx, row)
}

// A validated destination protects the held source during cleanup.
func (s *SourceMutationService) publishSourceMove(ctx context.Context, row *sourceMutationRow) error {
	plan := &row.Plan
	if sourceMutationPathExists(plan.FromAbs) {
		return ErrSourceMutationDiverged
	}
	heldExists := sourceMutationPathExists(plan.HoldAbs)
	published := sourceMutationPathExists(plan.ToAbs)
	target := plan.ToAbs
	if !published {
		if !heldExists || plan.MoveCleanupStarted {
			return ErrSourceMutationDiverged
		}
		if err := requireSourceIdentity(plan.StageAbs, plan.StageIdentity); err != nil {
			return err
		}
		target = filepath.Join(plan.StageAbs, "entry")
	}
	if err := requireSourceIdentity(target, plan.DestinationIdentity); err != nil {
		return err
	}
	// Partial cleanup leaves only the destination available for full validation.
	if err := requireSourceFingerprint(ctx, target, plan.TreeSHA); err != nil {
		return err
	}
	if heldExists {
		if err := requireSourceIdentity(plan.HoldAbs, plan.EntryIdentity); err != nil {
			return err
		}
		if !plan.MoveCleanupStarted {
			if err := requireSourceFingerprint(ctx, plan.HoldAbs, plan.TreeSHA); err != nil {
				return err
			}
		}
	}
	if !published {
		if err := s.publishSourceStage(ctx, row, plan.ToPath); err != nil {
			return err
		}
	}
	if err := requireSourceIdentity(plan.ToAbs, plan.DestinationIdentity); err != nil {
		return err
	}
	if heldExists {
		if !plan.MoveCleanupStarted {
			plan.MoveCleanupStarted = true
			if err := s.update(ctx, row); err != nil {
				return err
			}
		}
		heldRel, err := sourceRelativePath(plan.RootPath, plan.HoldAbs)
		if err != nil {
			return err
		}
		if err := fseffect.RemoveTreeGuarded(fseffect.Location{Root: plan.RootPath, Rel: heldRel}, plan.EntryIdentity); err != nil {
			return err
		}
	}
	if plan.StageAbs != "" {
		if err := s.removeSourceStage(plan); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// SourceMoveIncompleteError keeps an interrupted cross-volume move reviewable.
type SourceMoveIncompleteError struct {
	Cause    error
	HeldPath string
}

func (e *SourceMoveIncompleteError) Error() string {
	return fmt.Sprintf("the move needs recovery; retained source: %s: %v", e.HeldPath, e.Cause)
}
func (e *SourceMoveIncompleteError) Unwrap() error { return e.Cause }
