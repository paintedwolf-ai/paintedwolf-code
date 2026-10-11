package projectsource

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/lycaon/lycaon/internal/desktoptrash"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fspath"
)

type SourceEffects struct {
	Journal        *SourceMutationJournal
	recovery       *sourceRecovery
	trash          func(context.Context, string) (desktoptrash.Receipt, error)
	sameFilesystem func(string, string) (bool, error)
}

// SetTrashMover installs the platform adapter before the service starts.
func (s *SourceEffects) SetTrashMover(move func(context.Context, string) (desktoptrash.Receipt, error)) {
	if move != nil {
		s.trash = move
	}
}

func (s *SourceEffects) applyMutation(ctx context.Context, row *sourceMutationRow) error {
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

// applySourceDelete selects native recovery or the retained recovery promised by the plan.
func (s *SourceEffects) applySourceDelete(ctx context.Context, row *sourceMutationRow) error {
	plan := &row.Plan
	if plan.NativeTrash != nil {
		return s.applyNativeTrash(ctx, row)
	}
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
		if err := s.Journal.update(ctx, row); err != nil {
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
func (s *SourceEffects) disposeSourceEntry(ctx context.Context, plan *sourceMutationPlan, identity string) error {
	switch plan.Disposal {
	case sourceDisposalTrash:
		_, err := s.trash(ctx, plan.AbsPath)
		return err
	case sourceDisposalDiscard:
		return fseffect.RemoveTreeGuarded(fseffect.Location{Root: plan.RootPath, Rel: filepath.FromSlash(plan.Path)}, identity)
	default:
		return fmt.Errorf("%w: unknown disposal %q", ErrSourceMutationDiverged, plan.Disposal)
	}
}

// Replay requires the original attached root.
func (s *SourceMutationService) validateLifecycleReplay(ctx context.Context, operationID string, p ProjectSource) error {
	row, found, err := s.Journal.load(ctx, operationID)
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

func (s *SourceEffects) startSourceEffect(ctx context.Context, row *sourceMutationRow) error {
	if err := beginSourceEffect(ctx); err != nil {
		return err
	}
	row.Plan.EffectStarted = true
	return s.Journal.update(ctx, row)
}

var ErrSourceTrashUnavailable = errors.New("the item is no longer available in Trash")

// Presence selects native recovery. Absence preserves shipped retained recovery.
type sourceTrashRecovery struct {
	Receipt     desktoptrash.Receipt `json:"receipt"`
	RecoveryKey string               `json:"recovery_key,omitempty"`
}

func (s *SourceEffects) applyNativeTrash(ctx context.Context, row *sourceMutationRow) error {
	plan := &row.Plan
	identity, err := fspath.EntryIdentity(plan.AbsPath)
	if os.IsNotExist(err) && plan.DeleteStarted {
		if !nativeTrashReceiptRecorded(plan) {
			return ErrSourceTrashUnavailable
		}
		return nil
	}
	if err != nil {
		return err
	}
	if identity != plan.EntryIdentity {
		return ErrSourceMutationDiverged
	}
	resolved, _, err := resolveLifecyclePath(plan.RootPath, plan.Path)
	if err != nil {
		return err
	}
	if resolved != plan.AbsPath {
		return ErrSourceMutationDiverged
	}
	if err := s.startSourceEffect(ctx, row); err != nil {
		return err
	}
	plan.DeleteStarted, plan.DeleteIdentity = true, identity
	plan.NativeTrash.RecoveryKey = row.ID
	plan.NativeTrash.Receipt = desktoptrash.Receipt{}
	if err := s.Journal.update(ctx, row); err != nil {
		return err
	}
	receipt, moveErr := s.trash(ctx, plan.AbsPath)
	plan.NativeTrash.Receipt = receipt
	// Persist the recovery location even if the native service lost its final acknowledgement.
	if err := s.Journal.update(ctx, row); err != nil {
		return err
	}
	current, statErr := fspath.EntryIdentity(plan.AbsPath)
	if os.IsNotExist(statErr) || (statErr == nil && current != identity) {
		return nil
	}
	if moveErr == nil {
		moveErr = fmt.Errorf("system trash left the selected item in place")
	}
	return &SourceTrashFailedError{Cause: moveErr}
}

func (s *SourceEffects) restoreNativeTrash(ctx context.Context, row *sourceMutationRow) error {
	plan := &row.Plan
	if sourceMutationPathExists(plan.AbsPath) {
		if !plan.EffectStarted {
			return ErrSourceExists
		}
		return requireSourceIdentity(plan.AbsPath, plan.DestinationIdentity)
	}
	plan.DestinationIdentity = plan.NativeTrash.Receipt.Identity
	if err := s.startSourceEffect(ctx, row); err != nil {
		return err
	}
	err := desktoptrash.Restore(ctx, plan.NativeTrash.Receipt, fseffect.Location{Root: plan.RootPath, Rel: filepath.FromSlash(plan.Path)})
	if errors.Is(err, desktoptrash.ErrUnavailable) {
		return fmt.Errorf("%w: %w", ErrSourceTrashUnavailable, err)
	}
	return err
}

// A completed native move needs its acknowledged location before recovery can promise Undo.
func nativeTrashReceiptRecorded(plan *sourceMutationPlan) bool {
	receipt := plan.NativeTrash.Receipt
	return receipt.FormatVersion == 1 && receipt.Platform == runtime.GOOS && filepath.IsAbs(receipt.Path) && receipt.Identity != ""
}
