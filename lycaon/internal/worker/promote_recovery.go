package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/filelock"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/pkg/api"
)

// recoverInterruptedPromote rolls back an expired promotion.
func (s *MergeService) recoverInterruptedPromote(ctx context.Context, task *api.WorkerTask, jobID string) error {
	reclaimed, err := s.reclaimOrphanedApply(ctx, task, jobID)
	if err != nil {
		return err
	}
	if !reclaimed {
		return s.reject(OverlayPromoteNotPendingCode, map[string]any{
			"job_id": jobID,
			"reason": "another promote is applying this overlay",
		})
	}
	return nil
}

// reclaimOrphanedApply restores one expired promotion.
func (s *MergeService) reclaimOrphanedApply(ctx context.Context, task *api.WorkerTask, jobID string) (bool, error) {
	if s.Store == nil {
		return false, fmt.Errorf("recover interrupted promote: merge store not configured")
	}
	token, ok, err := s.Store.ReclaimExpiredMergeApply(ctx, jobID)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	roots := s.taskRootRefs(ctx, task)
	promote := PromoteRootsForTask(task, roots)
	if err := session.RollbackTaskCheckpointJournal(ctx, task, roots); err != nil {
		return true, fmt.Errorf("recover checkpoint promotion: %w", err)
	}
	found, err := recoverPromoteTransaction(s.hostDataDirFor(task), jobID, promote, task)
	if err != nil {
		return true, fmt.Errorf("recover interrupted promote: %w", err)
	}
	cleanupCtx, cancel := promoteCleanupContext(ctx)
	err = s.Store.ReleaseMergeApply(cleanupCtx, jobID, token)
	cancel()
	if err != nil {
		return true, fmt.Errorf("recover interrupted promote status: %w", err)
	}
	if found {
		if err := removePromoteTransaction(promoteTransactionPath(s.hostDataDirFor(task), jobID)); err != nil {
			slog.WarnContext(ctx, "recovered promote journal cleanup deferred", "job_id", jobID, "err", err)
		}
	}
	return true, nil
}

func failPromoteTransaction(ctx context.Context, tx *promoteTransaction, promote PromoteRoots, task *api.WorkerTask, revertPending func() error, cause error) error {
	var unstarted *promoteTransactionUnstartedError
	if errors.As(cause, &unstarted) {
		return errors.Join(cause, tx.close(), revertPending())
	}
	cleanupCtx, cancel := promoteCleanupContext(ctx)
	defer cancel()
	if err := session.RollbackTaskCheckpointJournal(cleanupCtx, task, promote.Roots); err != nil {
		return errors.Join(cause, err)
	}
	rollbackErr := tx.rollback(promote, task)
	if rollbackErr != nil {
		return errors.Join(cause, rollbackErr)
	}
	closeErr := tx.close()
	return errors.Join(cause, closeErr, revertPending())
}

func closeCommittedPromoteTransaction(ctx context.Context, tx *promoteTransaction, jobID string) {
	if err := tx.close(); err != nil {
		slog.WarnContext(ctx, "committed promote journal cleanup deferred", "job_id", jobID, "err", err)
	}
}

// settleCommittedPromotes finishes the close a crash interrupted after
// CommitPromotion. The job row is the commit fact: any other status keeps its journal.
func settleCommittedPromotes(ctx context.Context, jobs MergeQueue, dataDir string) {
	dirs, err := project.HostDataDirs(dataDir)
	if err != nil {
		slog.WarnContext(ctx, "committed promote settlement deferred", "err", err)
		return
	}
	for _, dir := range dirs {
		// Most projects have never promoted; do not leave a lock file in each of them.
		if _, err := os.Lstat(filepath.Join(dir, promoteTransactionsDirName)); err != nil {
			continue
		}
		lock, err := acquirePromoteFileLock(ctx, dir)
		if err != nil {
			slog.WarnContext(ctx, "committed promote settlement deferred", "dir", dir, "err", err)
			continue
		}
		settleCommittedPromotesIn(ctx, jobs, dir)
		lock.release()
	}
}

func settleCommittedPromotesIn(ctx context.Context, jobs MergeQueue, dir string) {
	journals := filepath.Join(dir, promoteTransactionsDirName)
	entries, err := os.ReadDir(journals)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			slog.WarnContext(ctx, "committed promote settlement deferred", "dir", journals, "err", err)
		}
		return
	}
	for _, entry := range entries {
		name, path := entry.Name(), filepath.Join(journals, entry.Name())
		var err error
		switch {
		case entry.IsDir() && strings.HasSuffix(name, promoteRetainedSuffix):
			// Journals precede slots and outlive them, so slots alone carry no authority.
			if _, statErr := os.Lstat(strings.TrimSuffix(path, promoteRetainedSuffix) + promoteJournalSuffix); errors.Is(statErr, os.ErrNotExist) {
				err = os.RemoveAll(path)
			}
		case strings.HasSuffix(name, promoteJournalSuffix) && !strings.HasPrefix(name, "."):
			err = settleJournalIfCommitted(jobs, dir, path)
		}
		if err != nil {
			slog.WarnContext(ctx, "committed promote settlement deferred", "journal", path, "err", err)
		}
	}
}

func settleJournalIfCommitted(jobs MergeQueue, dir, path string) error {
	journal, found, err := readPromoteTransaction(path)
	if err != nil || !found {
		return err
	}
	if promoteTransactionPath(dir, journal.JobID) != path {
		return fmt.Errorf("promote transaction %q is not bound to its job", journal.JobID)
	}
	task, ok := jobs.Get(journal.JobID)
	if !ok || task == nil || task.MergeStatus != api.WorkerMergeStatusMerged {
		return nil
	}
	return removePromoteTransaction(path)
}

type promoteFileLock struct {
	file *os.File
}

func acquirePromoteFileLock(ctx context.Context, dataDir string) (*promoteFileLock, error) {
	path := filepath.Join(dataDir, "promote.lock")
	file, err := filelock.Open(path)
	if err != nil {
		return nil, err
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		held, lockErr := filelock.TryExclusive(file)
		if lockErr != nil {
			_ = file.Close()
			return nil, lockErr
		}
		if held {
			return &promoteFileLock{file: file}, nil
		}
		select {
		case <-ctx.Done():
			_ = file.Close()
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (l *promoteFileLock) release() {
	if l == nil || l.file == nil {
		return
	}
	_ = filelock.Unlock(l.file)
	_ = l.file.Close()
}
