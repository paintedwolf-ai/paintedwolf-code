package project

import (
	"context"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/fseffect"
)

func (s *SourceMutationService) publishSourceStage(ctx context.Context, row *sourceMutationRow, to string) error {
	plan := &row.Plan
	entry := filepath.Join(plan.StageAbs, "entry")
	from, err := sourceRelativePath(plan.RootPath, entry)
	if err != nil {
		return err
	}
	if plan.EntryKind == SourceEntryFolder && plan.PublicationMode == 0 {
		info, err := os.Lstat(entry)
		if err != nil {
			return err
		}
		plan.PublicationMode = uint32(info.Mode())
		if err := s.update(ctx, row); err != nil {
			return err
		}
	}
	if err := s.startSourceEffect(ctx, row); err != nil {
		return err
	}
	if plan.EntryKind == SourceEntryFolder {
		return fseffect.PublishDirectory(plan.RootPath, from, to, plan.DestinationIdentity, os.FileMode(plan.PublicationMode))
	}
	return fseffect.RenameGuarded(plan.RootPath, from, to, plan.DestinationIdentity)
}

func restoreSourcePublicationMode(plan *sourceMutationPlan) error {
	if plan.PublicationMode == 0 {
		return nil
	}
	entry := filepath.Join(plan.StageAbs, "entry")
	target := plan.ToAbs
	if plan.Kind == "restore" {
		target = plan.AbsPath
	}
	if sourceMutationPathExists(target) {
		entry = target
	} else if !sourceMutationPathExists(entry) {
		return nil
	}
	rel, err := sourceRelativePath(plan.RootPath, entry)
	if err != nil {
		return err
	}
	return fseffect.RestoreDirectoryMode(fseffect.Location{Root: plan.RootPath, Rel: rel}, plan.DestinationIdentity, os.FileMode(plan.PublicationMode))
}
