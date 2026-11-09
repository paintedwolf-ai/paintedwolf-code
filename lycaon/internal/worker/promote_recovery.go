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
	"github.com/lycaon/lycaon/internal/fssync"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
	"github.com/lycaon/lycaon/internal/project"
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
	if _, err := recoverPromoteTransaction(ctx, s.hostDataDirFor(task), jobID, promote, task); err != nil {
		return true, fmt.Errorf("recover interrupted promote: %w", err)
	}
	cleanupCtx, cancel := promoteCleanupContext(ctx)
	err = s.Store.ReleaseMergeApply(cleanupCtx, jobID, token)
	cancel()
	if err != nil {
		return true, fmt.Errorf("recover interrupted promote status: %w", err)
	}
	return true, nil
}

// failPromoteTransaction rolls the target back before the job returns to pending. If rollback
// cannot finish the job stays applying, so its expired lease brings recovery back to the record.
func failPromoteTransaction(ctx context.Context, tx *promoteTransaction, revertPending func() error, cause error) error {
	cleanupCtx, cancel := promoteCleanupContext(ctx)
	defer cancel()
	if err := tx.rollback(cleanupCtx); err != nil {
		return errors.Join(cause, err)
	}
	return errors.Join(cause, revertPending())
}

func closeCommittedPromoteTransaction(ctx context.Context, tx *promoteTransaction, jobID string) {
	if err := tx.close(); err != nil {
		slog.WarnContext(ctx, "committed promote journal cleanup deferred", "job_id", jobID, "err", err)
	}
	if tx.afterCommit != nil {
		if err := tx.afterCommit(); err != nil {
			slog.WarnContext(ctx, "committed promote settlement deferred", "job_id", jobID, "err", err)
		}
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
		case entry.IsDir() && strings.HasSuffix(name, promoteTransactionSuffix):
			err = settleTransactionIfCommitted(jobs, dir, path)
		case !entry.IsDir() && strings.HasSuffix(name, shippedPromoteJournal) && !strings.HasPrefix(name, "."):
			err = settleShippedJournalIfCommitted(jobs, dir, path)
		}
		if err != nil {
			slog.WarnContext(ctx, "committed promote settlement deferred", "journal", path, "err", err)
		}
	}
}

func settleTransactionIfCommitted(jobs MergeQueue, dir, path string) error {
	id, found, err := checkpoint.ReadTransactionID(path)
	if err != nil {
		return err
	}
	if !found {
		// A close removes the journal first, so what remains carries no authority.
		return os.RemoveAll(path)
	}
	jobID, ok := jobIDFromPromoteTransactionID(id)
	if !ok || promoteTransactionDir(dir, jobID) != path {
		return fmt.Errorf("promote transaction %q is not bound to its job", id)
	}
	task, ok := jobs.Get(jobID)
	if !ok || task == nil || task.MergeStatus != api.WorkerMergeStatusMerged {
		return nil
	}
	return checkpoint.RemoveTransactionRecord(path)
}

func settleShippedJournalIfCommitted(jobs MergeQueue, dir, path string) error {
	journal, found, err := readShippedPromoteJournal(path)
	if err != nil || !found {
		return err
	}
	if shippedPromoteJournalPath(dir, journal.JobID) != path {
		return fmt.Errorf("promote transaction %q is not bound to its job", journal.JobID)
	}
	task, ok := jobs.Get(journal.JobID)
	if !ok || task == nil || task.MergeStatus != api.WorkerMergeStatusMerged {
		return nil
	}
	return removeShippedPromoteJournal(path)
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

// syncPromoteTransactionDir makes a journal removal durable.
func syncPromoteTransactionDir(dir *os.File) error {
	if err := fssync.File(dir); err != nil {
		return fmt.Errorf("sync promote transaction directory: %w", err)
	}
	return nil
}

func (l *promoteFileLock) release() {
	if l == nil || l.file == nil {
		return
	}
	_ = filelock.Unlock(l.file)
	_ = l.file.Close()
}
