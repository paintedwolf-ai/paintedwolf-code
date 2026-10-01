package project

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fspath"
)

// SetTrashMover installs the platform adapter before the service starts.
func (s *SourceMutationService) SetTrashMover(move func(context.Context, string) error) {
	if move != nil {
		s.trash = move
	}
}

func (s *SourceMutationService) applyMutation(ctx context.Context, row *sourceMutationRow) error {
	if err := restoreSourcePublicationMode(&row.Plan); err != nil {
		return err
	}
	switch row.Plan.Kind {
	case "delete":
		return s.applySourceDelete(ctx, row)
	case "restore":
		return s.applySourceRestore(ctx, row)
	case "rename":
		return s.applySourceMove(ctx, row)
	case "copy":
		return s.applySourceCopy(ctx, row)
	default:
		if err := s.startSourceEffect(ctx, row); err != nil {
			return err
		}
		return applySourceMutation(ctx, &row.Plan)
	}
}

// applySourceDelete disposes of an entry whose recovery copy is already kept.
func (s *SourceMutationService) applySourceDelete(ctx context.Context, row *sourceMutationRow) error {
	plan := &row.Plan
	if plan.RecoveryCount == 0 {
		return ErrSourceRecoveryFailed
	}
	identity, err := fspath.EntryIdentity(plan.AbsPath)
	if os.IsNotExist(err) && plan.DeleteStarted {
		return nil
	}
	if err != nil {
		return err
	}
	if plan.DeleteStarted && identity != plan.DeleteIdentity {
		return ErrSourceMutationDiverged
	}
	resolved, _, err := resolveLifecyclePath(plan.RootPath, plan.Path)
	if err != nil {
		return err
	}
	if resolved != plan.AbsPath {
		return ErrSourceMutationDiverged
	}
	if err := requireSourceFingerprint(ctx, plan.AbsPath, plan.TreeSHA); err != nil {
		return err
	}
	if err := s.startSourceEffect(ctx, row); err != nil {
		return err
	}
	if !plan.DeleteStarted {
		plan.DeleteStarted, plan.DeleteIdentity = true, identity
		if err := s.update(ctx, row); err != nil {
			return err
		}
	}
	disposeErr := s.disposeSourceEntry(ctx, plan, identity)
	current, statErr := fspath.EntryIdentity(plan.AbsPath)
	if os.IsNotExist(statErr) || (statErr == nil && current != identity) {
		// A concurrent replacement belongs to its writer.
		return nil
	}
	if plan.Disposal == sourceDisposalDiscard {
		if disposeErr == nil {
			disposeErr = fmt.Errorf("removal left the selected item in place")
		}
		return disposeErr
	}
	if disposeErr == nil {
		disposeErr = fmt.Errorf("system trash left the selected item in place")
	}
	return &SourceTrashFailedError{Cause: disposeErr}
}

// Trash receives the original path to retain its restore destination; a
// discarded entry is removed through held descriptors bound to its identity.
func (s *SourceMutationService) disposeSourceEntry(ctx context.Context, plan *sourceMutationPlan, identity string) error {
	switch plan.Disposal {
	case sourceDisposalTrash:
		return s.trash(ctx, plan.AbsPath)
	case sourceDisposalDiscard:
		return fseffect.RemoveTreeGuarded(fseffect.Location{Root: plan.RootPath, Rel: filepath.FromSlash(plan.Path)}, identity)
	default:
		return fmt.Errorf("%w: unknown disposal %q", ErrSourceMutationDiverged, plan.Disposal)
	}
}

// Replay requires the original attached root.
func (s *SourceMutationService) validateLifecycleReplay(ctx context.Context, operationID string, p *Project) error {
	row, found, err := s.load(ctx, operationID)
	if err != nil {
		return err
	}
	if !found || row.Status == sourceMutationCommitted {
		return nil
	}
	if err := validateSourceHistoryPlan(p, row.Plan); err != nil {
		return fmt.Errorf("%w: %w", ErrSourceMutationDiverged, err)
	}
	return nil
}

func sourceLifecycleKind(kind string) bool {
	switch kind {
	case "create", "rename", "copy", "delete", "restore":
		return true
	default:
		return false
	}
}

func (s *SourceMutationService) startSourceEffect(ctx context.Context, row *sourceMutationRow) error {
	if err := beginSourceEffect(ctx); err != nil {
		return err
	}
	row.Plan.EffectStarted = true
	return s.update(ctx, row)
}
