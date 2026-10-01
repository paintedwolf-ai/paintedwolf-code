package project

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/fssync"
	"github.com/lycaon/lycaon/internal/sourceledger"
)

var ErrSourceRecoveryFailed = errors.New("could not preserve file recovery data")

// captureRecovery keeps exact entries, including ignored files and empty folders.
// Unsupported special files stop the operation before any entry moves.
func (s *SourceMutationService) captureRecovery(ctx context.Context, plan *sourceMutationPlan, abs string) error {
	rel, err := sourceRelativePath(plan.RootPath, abs)
	if err != nil || !filepath.IsLocal(rel) {
		return ErrSourcePathDenied
	}
	scope, base, err := recoveryScope(plan.RootPath, rel)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrSourceRecoveryFailed, err)
	}
	defer func() { _ = scope.Close() }()
	retained, err := s.beginRecoveryCapture(ctx, plan, "preserving")
	if err != nil {
		return fmt.Errorf("%w: %w", ErrSourceRecoveryFailed, err)
	}
	defer retained.close()

	capture := func(current string, _ fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(base, filepath.FromSlash(current))
		if err != nil {
			return err
		}
		info, err := scope.Lstat(current)
		if err != nil {
			return err
		}
		saved := sourceledger.RecoveryEntry{Path: filepath.ToSlash(rel), Mode: uint32(info.Mode())}
		switch {
		case info.Mode().IsRegular():
		case info.Mode()&os.ModeSymlink != 0:
			saved.Link, err = scope.Readlink(current)
		case info.IsDir():
		default:
			err = fmt.Errorf("cannot preserve special file %s", rel)
		}
		if err != nil {
			return err
		}
		_, err = retained.append(ctx, saved, scope, current, nil)
		if err != nil {
			return err
		}
		return nil
	}
	if base == "." {
		err = fs.WalkDir(scope.FS(), ".", capture)
	} else {
		err = capture(base, nil, nil)
	}

	if err == nil {
		err = retained.finish(ctx)
	}
	if err != nil {
		return fmt.Errorf("%w: %w", ErrSourceRecoveryFailed, err)
	}
	return nil
}

func (s *SourceMutationService) copyRecoveryFile(ctx context.Context, sha string, dst io.Writer) error {
	if s.ledger != nil {
		return s.ledger.CopyRecoveryFile(ctx, sha, dst)
	}
	s.stateMu.Lock()
	content, ok := s.recoveryBytes[sha]
	s.stateMu.Unlock()
	if !ok {
		return fmt.Errorf("retained content unavailable")
	}
	_, err := dst.Write(content)
	return err
}

func (s *SourceMutationService) applySourceRestore(ctx context.Context, row *sourceMutationRow) error {
	plan := &row.Plan
	if sourceMutationPathExists(plan.AbsPath) {
		if !plan.EffectStarted {
			return ErrSourceExists
		}
		if err := requireSourceIdentity(plan.AbsPath, plan.DestinationIdentity); err != nil {
			return err
		}
		return requireSourceFingerprint(ctx, plan.AbsPath, plan.TreeSHA)
	}
	if plan.RecoveryCount == 0 {
		return ErrSourceRecoveryFailed
	}
	stageScope, err := s.openSourceStage(ctx, row, plan.AbsPath)
	if err != nil {
		return err
	}
	defer func() { _ = stageScope.Close() }()
	target := filepath.Join(plan.StageAbs, "entry")
	if err := s.restoreRecoveryManifest(ctx, plan, stageScope, "entry"); err != nil {
		return fmt.Errorf("%w: %w", ErrSourceRecoveryFailed, err)
	}
	reportSourceProgress(ctx, "verifying", plan.RecoveryCount, 0)
	if err := requireSourceFingerprint(ctx, target, plan.TreeSHA); err != nil {
		return err
	}
	plan.DestinationIdentity, err = fspath.EntryIdentity(target)
	if err != nil {
		return err
	}
	err = s.publishSourceStage(ctx, row, plan.Path)
	if os.IsExist(err) {
		return ErrSourceMutationDiverged
	}
	if err != nil {
		return err
	}
	return s.removeSourceStage(plan)
}

func (s *SourceMutationService) walkRecovery(ctx context.Context, plan *sourceMutationPlan, reverse bool, visit func(sourceledger.RecoveryEntry) error) error {
	if s.ledger != nil {
		return s.ledger.WalkRecovery(ctx, plan.ProjectID, plan.RecoveryID, plan.RecoveryCount, reverse, visit)
	}
	s.stateMu.Lock()
	entries := s.recoveryEntries[plan.RecoveryID]
	s.stateMu.Unlock()
	if int64(len(entries)) != plan.RecoveryCount {
		return ErrSourceRecoveryFailed
	}
	for i := range entries {
		index := i
		if reverse {
			index = len(entries) - 1 - i
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := visit(entries[index]); err != nil {
			return err
		}
	}
	return nil
}

func (s *SourceMutationService) restoreRecoveryManifest(ctx context.Context, plan *sourceMutationPlan, scope *os.Root, base string) error {
	progress := newSourceWorkProgress(ctx, "restoring")
	if err := s.walkRecovery(ctx, plan, false, func(entry sourceledger.RecoveryEntry) error {
		if err := s.restoreRecoveryEntry(ctx, entry, scope, base, progress); err != nil {
			return err
		}
		progress.entry()
		return nil
	}); err != nil {
		return err
	}
	progress.report(true)
	return s.walkRecovery(ctx, plan, true, func(entry sourceledger.RecoveryEntry) error { return restoreRecoveryMode(entry, scope, base) })
}

func (s *SourceMutationService) restoreRecoveryEntry(ctx context.Context, entry sourceledger.RecoveryEntry, scope *os.Root, base string, progress *sourceWorkProgress) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if entry.Path != "." && !filepath.IsLocal(filepath.FromSlash(entry.Path)) {
		return ErrSourcePathDenied
	}
	path := filepath.Join(base, filepath.FromSlash(entry.Path))
	mode := os.FileMode(entry.Mode)
	switch {
	case mode.IsDir():
		return scope.Mkdir(path, 0o700)
	case mode&os.ModeSymlink != 0:
		return scope.Symlink(entry.Link, path)
	case mode.IsRegular():
		return s.restoreRecoveryFile(ctx, scope, path, mode, entry.SHA, progress)
	default:
		return ErrSourceRecoveryFailed
	}
}

func restoreRecoveryMode(entry sourceledger.RecoveryEntry, scope *os.Root, base string) error {
	if entry.Path != "." && !filepath.IsLocal(filepath.FromSlash(entry.Path)) {
		return ErrSourcePathDenied
	}
	if os.FileMode(entry.Mode).IsDir() {
		dir, err := scope.Open(filepath.Join(base, filepath.FromSlash(entry.Path)))
		if err != nil {
			return err
		}
		syncErr := dir.Chmod(os.FileMode(entry.Mode))
		if syncErr == nil {
			syncErr = fseffect.SyncDirectory(scope, filepath.Join(base, filepath.FromSlash(entry.Path)))
		}
		return errors.Join(syncErr, dir.Close())
	}
	return nil
}

func (s *SourceMutationService) restoreRecoveryFile(ctx context.Context, scope *os.Root, path string, mode os.FileMode, sha string, progress *sourceWorkProgress) error {
	f, err := scope.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	copyErr := s.copyRecoveryFile(ctx, sha, io.MultiWriter(f, progress))
	if copyErr == nil {
		copyErr = f.Chmod(mode)
	}
	if copyErr == nil {
		copyErr = fssync.File(f)
	}
	return errors.Join(copyErr, f.Close())
}

// Pending mutation plans protect partial manifests while preparation runs.
func (s *SourceMutationService) pruneSourceRecovery(ctx context.Context, projectID string) error {
	if s.db == nil {
		return nil
	}
	return pruneSourceRecoveryRows(ctx, s.db, projectID)
}

func pruneSourceRecoveryRows(ctx context.Context, exec interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, projectID string) error {
	for _, table := range []string{"source_recovery_objects", "source_recovery_entries"} {
		_, err := exec.ExecContext(ctx, `DELETE FROM `+table+`
 WHERE (?='' OR project_id=?)
 AND NOT EXISTS (SELECT 1 FROM source_history_entries h WHERE h.project_id=`+table+`.project_id AND json_extract(h.undo_plan_json,'$.recovery_id')=`+table+`.recovery_id)
 AND NOT EXISTS (SELECT 1 FROM source_mutations m WHERE m.project_id=`+table+`.project_id AND m.status IN ('prepared','file_applied','failed','diverged') AND json_extract(m.plan_json,'$.recovery_id')=`+table+`.recovery_id)`, projectID, projectID)
		if err != nil {
			return err
		}
	}
	return nil
}

// Traversal holds the selected directory and preserves symbolic links.
func recoveryScope(rootPath, path string) (*os.Root, string, error) {
	scope, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, "", err
	}
	base := filepath.FromSlash(path)
	info, err := scope.Lstat(base)
	if err != nil {
		_ = scope.Close()
		return nil, "", err
	}
	if !info.IsDir() {
		return scope, base, nil
	}
	selected, err := scope.OpenRoot(base)
	_ = scope.Close()
	if err != nil {
		return nil, "", err
	}
	opened, err := selected.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		_ = selected.Close()
		if err != nil {
			return nil, "", err
		}
		return nil, "", ErrSourceMutationDiverged
	}
	return selected, ".", nil
}
