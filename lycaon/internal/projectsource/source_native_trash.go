package projectsource

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/desktoptrash"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fspath"
)

// Presence selects native recovery. Absence preserves shipped retained recovery.
type sourceTrashRecovery struct {
	Receipt desktoptrash.Receipt `json:"receipt"`
}

func (s *SourceEffects) applyNativeTrash(ctx context.Context, row *sourceMutationRow) error {
	plan := &row.Plan
	identity, err := fspath.EntryIdentity(plan.AbsPath)
	if os.IsNotExist(err) && plan.DeleteStarted { return nil }
	if err != nil { return err }
	if identity != plan.EntryIdentity { return ErrSourceMutationDiverged }
	resolved, _, err := resolveLifecyclePath(plan.RootPath, plan.Path)
	if err != nil { return err }
	if resolved != plan.AbsPath { return ErrSourceMutationDiverged }
	if err := s.startSourceEffect(ctx, row); err != nil { return err }
	plan.DeleteStarted, plan.DeleteIdentity = true, identity
	if err := s.Journal.update(ctx, row); err != nil { return err }
	receipt, moveErr := s.trash(ctx, plan.AbsPath)
	plan.NativeTrash.Receipt = receipt
	// Persist the recovery location even if the native service lost its final acknowledgement.
	if err := s.Journal.update(ctx, row); err != nil { return err }
	current, statErr := fspath.EntryIdentity(plan.AbsPath)
	if os.IsNotExist(statErr) || (statErr == nil && current != identity) { return nil }
	if moveErr == nil { moveErr = fmt.Errorf("system trash left the selected item in place") }
	return &SourceTrashFailedError{Cause: moveErr}
}

func (s *SourceEffects) restoreNativeTrash(ctx context.Context, row *sourceMutationRow) error {
	plan := &row.Plan
	if sourceMutationPathExists(plan.AbsPath) {
		if !plan.EffectStarted { return ErrSourceExists }
		return requireSourceIdentity(plan.AbsPath, plan.DestinationIdentity)
	}
	plan.DestinationIdentity = plan.NativeTrash.Receipt.Identity
	if err := s.startSourceEffect(ctx, row); err != nil { return err }
	return desktoptrash.Restore(ctx, plan.NativeTrash.Receipt, fseffect.Location{Root: plan.RootPath, Rel: filepath.FromSlash(plan.Path)})
}
