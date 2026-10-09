package projectsource

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/textfile"
)

func isSourceDivergence(err error) bool {
	return errors.Is(err, ErrSourceWriteConflict) || errors.Is(err, ErrSourceExists) ||
		errors.Is(err, ErrSourceNotFound) || errors.Is(err, ErrSourceMutationDiverged)
}

func applySourceMutation(ctx context.Context, plan *sourceMutationPlan) error {
	switch plan.Kind {
	case "write":
		return applyProjectSourceWrite(&sourceWritePlan{Result: SourceWriteResult{
			Path: plan.Path, RootID: plan.RootID, AbsPath: plan.AbsPath, SizeBytes: int64(len(plan.After)),
			SHA256: plan.AfterSHA, Before: plan.Before, After: plan.After, Changed: plan.Changed,
		}, BaseSHA256: plan.BaseSHA256, RootPath: plan.RootPath})
	case "create":
		return applySourceCreate(plan)
	case "rename":
		return applySourceRename(plan)
	case "batch_write":
		return applySourceBatchWrite(ctx, plan)
	default:
		return fmt.Errorf("unknown source mutation kind %q", plan.Kind)
	}
}

func verifySourceMutationApplied(ctx context.Context, plan *sourceMutationPlan) error {
	if plan == nil || !plan.Changed {
		return nil
	}
	switch plan.Kind {
	case "write":
		raw, err := os.ReadFile(plan.AbsPath)
		if err != nil || textfile.SHA256(raw) != plan.AfterSHA {
			return ErrSourceMutationDiverged
		}
		return nil
	case "batch_write":
		for i := range plan.Writes {
			if err := verifySourceMutationApplied(ctx, &plan.Writes[i]); err != nil {
				return fmt.Errorf("verify batch write %s: %w", plan.Writes[i].Path, err)
			}
		}
		return nil
	case "create":
		info, err := os.Lstat(plan.AbsPath)
		if err != nil {
			return ErrSourceMutationDiverged
		}
		if plan.EntryKind == SourceEntryFolder && info.IsDir() {
			entries, readErr := os.ReadDir(plan.AbsPath)
			if readErr == nil && len(entries) == 0 {
				return nil
			}
		}
		if plan.EntryKind == SourceEntryFile && info.Mode().IsRegular() {
			raw, readErr := os.ReadFile(plan.AbsPath)
			if readErr == nil && textfile.SHA256(raw) == plan.AfterSHA {
				return nil
			}
		}
		return ErrSourceMutationDiverged
	case "rename":
		if sourceMutationPathExists(plan.FromAbs) || !sourceMutationPathExists(plan.ToAbs) {
			return ErrSourceMutationDiverged
		}
		identity := plan.EntryIdentity
		if plan.CrossVolume {
			identity = plan.DestinationIdentity
		}
		return requireSourceIdentity(plan.ToAbs, identity)
	case "copy":
		if !sourceMutationPathExists(plan.ToAbs) {
			return ErrSourceMutationDiverged
		}
		if err := requireSourceIdentity(plan.ToAbs, plan.DestinationIdentity); err != nil {
			return err
		}
		return requireSourceFingerprint(ctx, plan.ToAbs, plan.TreeSHA)
	case "delete":
		if !plan.DeleteStarted {
			return ErrSourceMutationDiverged
		}
		return nil
	case "restore":
		if plan.NativeTrash != nil { return requireSourceIdentity(plan.AbsPath, plan.DestinationIdentity) }
		if err := requireSourceIdentity(plan.AbsPath, plan.DestinationIdentity); err != nil {
			return err
		}
		return requireSourceFingerprint(ctx, plan.AbsPath, plan.TreeSHA)
	default:
		return fmt.Errorf("verify unknown source mutation kind %q", plan.Kind)
	}
}

func applySourceBatchWrite(ctx context.Context, plan *sourceMutationPlan) error {
	applied := make([]int, 0, len(plan.Writes))
	for i := range plan.Writes {
		write := &plan.Writes[i]
		if !write.Changed {
			continue
		}
		beforeCurrent, readErr := os.ReadFile(write.AbsPath)
		wasApplied := readErr == nil && textfile.SHA256(beforeCurrent) == write.AfterSHA
		if err := applySourceMutation(ctx, write); err != nil {
			return errors.Join(err, rollbackSourceBatch(plan, applied))
		}
		if !wasApplied {
			applied = append(applied, i)
		}
	}
	return nil
}

func rollbackSourceBatch(plan *sourceMutationPlan, applied []int) error {
	var rollbackErr error
	for i := len(applied) - 1; i >= 0; i-- {
		write := plan.Writes[applied[i]]
		rollback := sourceWritePlan{Result: SourceWriteResult{
			Path: write.Path, RootID: write.RootID, AbsPath: write.AbsPath,
			SizeBytes: int64(len(write.Before)), SHA256: write.BaseSHA256,
			Before: write.After, After: write.Before, Changed: true,
		}, BaseSHA256: write.AfterSHA, RootPath: write.RootPath}
		if err := applyProjectSourceWrite(&rollback); err != nil {
			rollbackErr = errors.Join(rollbackErr, fmt.Errorf("rollback %s: %w", write.Path, err))
		}
	}
	return rollbackErr
}

func applySourceCreate(plan *sourceMutationPlan) error {
	info, err := os.Lstat(plan.AbsPath)
	if err == nil {
		if plan.EntryKind == SourceEntryFolder && info.IsDir() {
			entries, readErr := os.ReadDir(plan.AbsPath)
			if readErr == nil && len(entries) == 0 {
				return nil
			}
		}
		if plan.EntryKind == SourceEntryFile && info.Mode().IsRegular() {
			raw, readErr := os.ReadFile(plan.AbsPath)
			if readErr == nil && textfile.SHA256(raw) == plan.AfterSHA {
				return nil
			}
		}
		return ErrSourceExists
	}
	if !os.IsNotExist(err) {
		return err
	}
	if plan.EntryKind == SourceEntryFolder {
		return fseffect.MkdirAll(fseffect.Location{Root: plan.RootPath, Rel: filepath.FromSlash(plan.Path)}, sourceCreateDirMode)
	}
	if plan.CreateParents {
		parent := filepath.Dir(filepath.FromSlash(plan.Path))
		if parent != "." {
			if err := fseffect.MkdirAll(fseffect.Location{Root: plan.RootPath, Rel: parent}, sourceCreateDirMode); err != nil {
				return err
			}
		}
	}
	_, err = fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.Location{Root: plan.RootPath, Rel: filepath.FromSlash(plan.Path)},
		Source:   bytes.NewReader(plan.After), Mode: sourceCreateFileMode,
		BeforeCommit: func(target fseffect.Target, _ fseffect.Result) error {
			_, statErr := target.Lstat()
			if os.IsNotExist(statErr) {
				return nil
			}
			if statErr != nil {
				return statErr
			}
			return ErrSourceExists
		},
	})
	return err
}

func applySourceRename(plan *sourceMutationPlan) error {
	fromExists := sourceMutationPathExists(plan.FromAbs)
	toExists := sourceMutationPathExists(plan.ToAbs)
	if !fromExists && toExists {
		return requireSourceIdentity(plan.ToAbs, plan.EntryIdentity)
	}
	if !fromExists || toExists {
		return ErrSourceMutationDiverged
	}
	if err := requireSourceIdentity(plan.FromAbs, plan.EntryIdentity); err != nil {
		return err
	}
	return fseffect.RenameGuarded(plan.RootPath, filepath.FromSlash(plan.FromPath), filepath.FromSlash(plan.ToPath), plan.EntryIdentity)
}

func requireSourceIdentity(path, want string) error {
	got, err := fspath.EntryIdentity(path)
	if err != nil {
		return err
	}
	if want == "" || got != want {
		return ErrSourceMutationDiverged
	}
	return nil
}

func requireSourceFingerprint(ctx context.Context, path, want string) error {
	got, err := sourceTreeFingerprint(ctx, path)
	if err != nil {
		return err
	}
	if got != want {
		return ErrSourceMutationDiverged
	}
	return nil
}

func sourceMutationPathExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}
