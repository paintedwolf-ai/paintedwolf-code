package projectsource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/lycaon/lycaon/internal/desktoptrash"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/gitrepo"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/sourceeffect"
)

type SourceEffects struct {
	Journal        *SourceMutationJournal
	recovery       *sourceRecovery
	trash          func(context.Context, string) (desktoptrash.Receipt, error)
	sameFilesystem func(string, string) (bool, error)
	relocate       func(fseffect.Location, fseffect.Location, string) error
}

// SetTrashMover installs the platform adapter before the service starts.
func (s *SourceEffects) SetTrashMover(move func(context.Context, string) (desktoptrash.Receipt, error)) {
	if move != nil {
		s.trash = move
	}
}

// SetSameFilesystem installs the filesystem identity comparator before the service starts.
func (s *SourceEffects) SetSameFilesystem(same func(string, string) (bool, error)) {
	if same != nil {
		s.sameFilesystem = same
	}
}

func (s *SourceEffects) canMoveAside(rootPath, absPath string) (bool, error) {
	dir := settingsoverlay.RecoveryDir(rootPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false, err
	}
	_ = gitrepo.EnsureRecoveryExcluded(rootPath)
	same := s.sameFilesystem
	if same == nil {
		same = fspath.SameFilesystem
	}
	return same(absPath, dir)
}

func recoverySlotName(id string) string {
	digest := sha256.Sum256([]byte(id))
	return hex.EncodeToString(digest[:])
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
	if !plan.Unrecoverable && plan.RecoveryCount == 0 {
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
	if !plan.DeleteStarted && plan.Unrecoverable && plan.EntryIdentity != "" && identity != plan.EntryIdentity {
		return ErrSourceMutationDiverged
	}
	resolved, _, err := resolveLifecyclePath(plan.RootPath, plan.Path)
	if err != nil {
		return err
	}
	if resolved != plan.AbsPath {
		return ErrSourceMutationDiverged
	}
	if plan.TreeSHA != "" {
		if plan.Unrecoverable {
			if err := requireRemovalMetadata(ctx, plan.AbsPath, plan.TreeSHA, row.memory); err != nil {
				return err
			}
		} else if err := requireSourceFingerprint(ctx, plan.AbsPath, plan.TreeSHA, row.memory); err != nil {
			return err
		}
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
	if err := requireSourceIdentity(plan.AbsPath, identity); err != nil {
		return err
	}
	if plan.TreeSHA != "" {
		if plan.Unrecoverable {
			if err := requireRemovalMetadata(ctx, plan.AbsPath, plan.TreeSHA, row.memory); err != nil {
				return err
			}
		} else if err := requireSourceFingerprint(ctx, plan.AbsPath, plan.TreeSHA, row.memory); err != nil {
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
	Receipt        desktoptrash.Receipt `json:"receipt"`
	RecoveryKey    string               `json:"recovery_key,omitempty"`
	AllocatedBytes int64                `json:"allocated_bytes,omitempty"`
	SizeMeasured   bool                 `json:"size_measured,omitempty"`
	Reclaiming     bool                 `json:"reclaiming,omitempty"`
	Reclaimed      bool                 `json:"reclaimed,omitempty"`
}

func (s *SourceEffects) applyNativeTrash(ctx context.Context, row *sourceMutationRow) error {
	plan := &row.Plan
	recoveryDir := settingsoverlay.RecoveryDir(plan.RootPath)
	recoverySlot := recoverySlotName(row.ID)
	recoveryPath := filepath.Join(recoveryDir, recoverySlot)
	identity, err := fspath.EntryIdentity(plan.AbsPath)
	if os.IsNotExist(err) && plan.DeleteStarted {
		if !nativeTrashReceiptRecorded(plan) && plan.Disposal == sourceDisposalDiscard {
			if held, statErr := fspath.EntryIdentity(recoveryPath); statErr == nil && held == plan.DeleteIdentity {
				plan.NativeTrash.Receipt = desktoptrash.Receipt{
					FormatVersion: 1,
					Platform:      runtime.GOOS,
					Path:          recoveryPath,
					Identity:      held,
				}
				if updateErr := s.Journal.update(ctx, row); updateErr != nil {
					return updateErr
				}
			}
		}
		if !nativeTrashReceiptRecorded(plan) {
			return ErrSourceTrashUnavailable
		}
		return nil
	}
	if err != nil {
		if os.IsNotExist(err) && plan.Disposal == sourceDisposalDiscard {
			return ErrSourceMutationDiverged
		}
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
	if plan.Disposal == sourceDisposalDiscard {
		if sourceMutationPathExists(recoveryPath) {
			return ErrSourceMutationDiverged
		}
		same, sameErr := s.canMoveAside(plan.RootPath, plan.AbsPath)
		if sameErr != nil || !same {
			plan.NativeTrash, plan.EntryRecoveryKey, plan.RecoveryID = nil, "", ""
			if sameErr == nil {
				sameErr = errors.ErrUnsupported
			}
			return &sourceeffect.RecoveryUnavailableError{Cause: sameErr}
		}
	}
	if err := s.startSourceEffect(ctx, row); err != nil {
		return err
	}
	plan.DeleteStarted, plan.DeleteIdentity = true, identity
	plan.NativeTrash.RecoveryKey = row.ID
	plan.NativeTrash.Receipt = desktoptrash.Receipt{}
	plan.NativeTrash.AllocatedBytes = 0
	plan.NativeTrash.SizeMeasured = false
	plan.NativeTrash.Reclaiming = false
	plan.NativeTrash.Reclaimed = false
	if err := s.Journal.update(ctx, row); err != nil {
		return err
	}
	if err := requireSourceIdentity(plan.AbsPath, identity); err != nil {
		plan.NativeTrash, plan.EntryRecoveryKey, plan.RecoveryID = nil, "", ""
		_ = s.Journal.update(context.WithoutCancel(ctx), row)
		return err
	}
	var receipt desktoptrash.Receipt
	var moveErr error
	if plan.Disposal == sourceDisposalDiscard {
		receipt, moveErr = desktoptrash.MoveTo(ctx, plan.AbsPath, desktoptrash.RecoveryArea(
			fseffect.Location{Root: plan.RootPath, Rel: filepath.FromSlash(plan.Path)},
			recoveryDir,
			recoverySlot,
			s.relocate,
		))
	} else {
		move := s.trash
		if move == nil {
			move = func(ctx context.Context, path string) (desktoptrash.Receipt, error) {
				return desktoptrash.MoveTo(ctx, path, desktoptrash.SystemTrash())
			}
		}
		receipt, moveErr = move(ctx, plan.AbsPath)
	}
	plan.NativeTrash.Receipt = receipt
	// Persist the recovery location even if the native service lost its final acknowledgement.
	if err := s.Journal.update(ctx, row); err != nil {
		return err
	}
	if plan.Disposal == sourceDisposalDiscard {
		current, currentErr := fspath.EntryIdentity(plan.AbsPath)
		published, publishedErr := fspath.EntryIdentity(recoveryPath)
		if publishedErr == nil && published == identity && (os.IsNotExist(currentErr) || (currentErr == nil && current != identity)) {
			if moveErr != nil {
				return &sourceeffect.AppliedError{OperationID: row.ID, Cause: moveErr}
			}
			return nil
		}
		if currentErr == nil && current == identity && os.IsNotExist(publishedErr) && !errors.Is(moveErr, fseffect.ErrPostcondition) && !os.IsExist(moveErr) {
			plan.NativeTrash, plan.EntryRecoveryKey, plan.RecoveryID = nil, "", ""
			_ = s.Journal.update(context.WithoutCancel(ctx), row)
			if ctx.Err() != nil {
				return &sourceeffect.RecoveryUnavailableError{Cause: ctx.Err()}
			}
			return &sourceeffect.RecoveryUnavailableError{Cause: moveErr}
		}
		return errors.Join(ErrSourceMutationDiverged, moveErr, currentErr, publishedErr)
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
	if plan.NativeTrash == nil || plan.NativeTrash.Reclaiming || plan.NativeTrash.Reclaimed {
		return ErrSourceTrashUnavailable
	}
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
	destination := fseffect.Location{Root: plan.RootPath, Rel: filepath.FromSlash(plan.Path)}
	var relocate func(fseffect.Location, fseffect.Location, string) error
	if plan.Disposal == sourceDisposalDiscard {
		relocate = s.relocate
	}
	err := desktoptrash.RestoreWith(ctx, plan.NativeTrash.Receipt, destination, relocate)
	if errors.Is(err, desktoptrash.ErrUnavailable) {
		return fmt.Errorf("%w: %w", ErrSourceTrashUnavailable, err)
	}
	if err != nil && plan.Disposal == sourceDisposalDiscard {
		current, currentErr := fspath.EntryIdentity(plan.NativeTrash.Receipt.Path)
		published, publishedErr := fspath.EntryIdentity(plan.AbsPath)
		if publishedErr == nil && published == plan.DestinationIdentity && (os.IsNotExist(currentErr) || (currentErr == nil && current != plan.DestinationIdentity)) {
			return &sourceeffect.AppliedError{OperationID: row.ID, Cause: err}
		}
		if errors.Is(err, fseffect.ErrPostcondition) || os.IsExist(err) {
			return errors.Join(ErrSourceMutationDiverged, err)
		}
	}
	if err != nil {
		return err
	}
	identity, err := fspath.EntryIdentity(plan.AbsPath)
	if err != nil {
		return err
	}
	plan.DestinationIdentity = identity
	return s.Journal.update(ctx, row)
}

// A completed native move needs its acknowledged location before recovery can promise Undo.
func nativeTrashReceiptRecorded(plan *sourceMutationPlan) bool {
	if plan == nil || plan.NativeTrash == nil {
		return false
	}
	receipt := plan.NativeTrash.Receipt
	return receipt.FormatVersion == 1 && receipt.Platform == runtime.GOOS && filepath.IsAbs(receipt.Path) && receipt.Identity != ""
}
