package sourcerewind

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/sourceeffect"
	"github.com/lycaon/lycaon/internal/sourcefeed"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/pkg/api"
)

// Apply persists a progress marker on both sides of each source mutation. A
// failure after a source receipt is committed remains recoverable by that ID.
func (s *Service) Apply(ctx context.Context, p *project.Project, o *Operation, save func() error) error {
	if err := o.ValidateWorkspace(p); err != nil {
		return err
	}
	fresh := o.Phase == "prepared"
	run := func() (err error) {
		defer func() {
			if value := recover(); value != nil {
				err = fmt.Errorf("panic applying source rewind: %v", value)
			}
			if err != nil {
				err = errors.Join(err, s.Rollback(ctx, o, save))
			}
		}()
		return s.apply(ctx, o, save)
	}
	if s.Documents != nil {
		if fresh {
			return s.Documents.WithSourceRewind(ctx, p, o.Selection(), run)
		}
		return s.Documents.RecoverSourceRewind(ctx, p, o.Selection(), run)
	}
	return run()
}

func (s *Service) RollbackProject(ctx context.Context, p *project.Project, o *Operation, save func() error) error {
	if o.Phase == "prepared" && o.Applied == 0 && !o.Pending {
		return s.Rollback(ctx, o, save)
	}
	if err := o.ValidateWorkspace(p); err != nil {
		return err
	}
	run := func() error { return s.Rollback(ctx, o, save) }
	if s.Documents != nil {
		return s.Documents.RollbackSourceRewind(ctx, p, o.Selection(), run)
	}
	return run()
}

func (s *Service) apply(ctx context.Context, o *Operation, save func() error) error {
	if o.Phase == "files_applied" {
		return nil
	}
	if o.Phase == "prepared" {
		// Validate the whole plan once more before the first mutation.
		for _, f := range o.Files {
			if err := matchState(f.RootPath, f.Expected); err != nil {
				return err
			}
		}
	}
	o.Phase = "applying"
	if err := save(); err != nil {
		return err
	}
	for o.Applied < len(o.Files) {
		i := o.Applied
		o.Pending = true
		if err := save(); err != nil {
			return err
		}
		if err := s.applyFile(ctx, o, i, false); err != nil {
			return err
		}
		o.Applied++
		o.Pending = false
		if err := save(); err != nil {
			return err
		}
	}
	o.Phase = "files_applied"
	return save()
}

// Rollback compensates only paths matching this operation's recorded states.
func (s *Service) Rollback(ctx context.Context, o *Operation, save func() error) error {
	ctx = context.WithoutCancel(ctx)
	if o.Pending && o.Applied < len(o.Files) {
		f := o.Files[o.Applied]
		if err := matchState(f.RootPath, f.Expected); err != nil {
			// Resolve an effect whose filesystem step completed before its
			// parent journal could advance; its stable receipt prevents duplicates.
			if err := s.applyFile(ctx, o, o.Applied, false); err != nil {
				return err
			}
			o.Applied++
		} else if f.Expected.Path != f.Target.Path {
			// A staged rename may have both endpoints present.
			if err := matchState(f.RootPath, f.Target); err == nil {
				if err := s.applyFile(ctx, o, o.Applied, false); err != nil {
					return err
				}
				o.Applied++
			}
		}
		o.Pending = false
		if err := save(); err != nil {
			return err
		}
	}
	o.Phase = "rolling_back"
	if err := save(); err != nil {
		return err
	}
	for o.Applied > 0 {
		if err := s.applyFile(ctx, o, o.Applied-1, true); err != nil {
			return err
		}
		o.Applied--
		if err := save(); err != nil {
			return err
		}
	}
	o.Phase = "rolled_back"
	return save()
}

func (s *Service) applyFile(ctx context.Context, o *Operation, index int, reverse bool) error {
	f := o.Files[index]
	before, after := f.Expected, f.Target
	cause := "session_rewind"
	if reverse {
		before, after = after, before
		cause = "session_rewind_rollback"
	}
	op := api.SourceChangeOpWrite
	if before.State == "absent" {
		op = api.SourceChangeOpCreate
	}
	if after.State == "absent" {
		op = api.SourceChangeOpDelete
	}
	if before.Path != after.Path {
		op = api.SourceChangeOpRename
	}
	operationID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("%s/%s/%d", o.AttemptID, cause, index))).String()
	loc, err := location(f.RootPath, after.Path)
	if err != nil {
		return err
	}
	effect := sourceeffect.Plan{
		Record: sourceledger.RecordInput{
			RecordLocation: sourceledger.RecordLocation{RootID: after.RootID, Path: after.Path, EntryKind: sourceledger.EntryKindFile}, ProjectID: o.ProjectID, BranchID: f.BranchID,
			FileID: f.FileID, DerivedFromVersionID: after.ID,
			Op: op, Origin: api.SourceChangeOriginUser, PersonID: o.PersonID, OperationID: operationID,
			BatchID: o.AttemptID, Cause: cause, Before: before.Content, After: after.Content,
			BeforeSHA256: before.SHA256, AfterSHA256: after.SHA256},
		Change: sourcefeed.Change{ProjectID: o.ProjectID, WorkspaceID: o.WorkspaceID, WorkspaceKind: api.SourceWorkspaceKindProject,
			RootID: after.RootID, Path: after.Path, Op: op, Origin: api.SourceChangeOriginUser,
			AfterSHA256: after.SHA256, AbsPath: filepath.Join(f.RootPath, filepath.FromSlash(after.Path))}, Target: loc,
	}
	if op == api.SourceChangeOpRename {
		effect.Record.FromRootID = before.RootID
		effect.Record.FromPath = before.Path
		effect.Change.FromPath = before.Path
		effect.Change.FromAbsPath = filepath.Join(f.RootPath, filepath.FromSlash(before.Path))
		effect.From = fseffect.Location{Root: f.RootPath, Rel: filepath.FromSlash(before.Path)}
	}
	pending, err := s.Mutations.PrepareEffect(ctx, effect)
	if err != nil {
		return err
	}
	applyErr := transition(f, before, after)
	return errors.Join(applyErr, pending.Finish(ctx, applyErr))
}

func (o *Operation) Verify() error {
	for _, f := range o.Files {
		if err := matchState(f.RootPath, f.Target); err != nil {
			return err
		}
		if f.Expected.Path != f.Target.Path {
			absent := f.Expected
			absent.State = "absent"
			absent.Content = nil
			absent.SHA256 = ""
			if err := matchState(f.RootPath, absent); err != nil {
				return err
			}
		}
	}
	return nil
}
