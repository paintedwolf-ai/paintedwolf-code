package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
	"github.com/lycaon/lycaon/pkg/api"
)

// promoteTransaction runs a promotion's exact byte plans through the one promotion transaction.
type promoteTransaction struct {
	txn   *checkpoint.Transaction
	plans []promoteMutation
	// afterCommit closes what the promotion joined, once its commit fact is durable.
	afterCommit func() error
}

// promoteTransactionEffects holds the seams a crash-ordering contract observes.
type promoteTransactionEffects = checkpoint.TransactionConfig

func beginPromoteTransaction(dataDir, jobID string, plans []promoteMutation) (*promoteTransaction, error) {
	return beginPromoteTransactionWith(dataDir, jobID, plans, promoteTransactionEffects{})
}

func beginPromoteTransactionWith(dataDir, jobID string, plans []promoteMutation, effects promoteTransactionEffects) (*promoteTransaction, error) {
	effects.Dir, effects.ID = promoteTransactionDir(dataDir, jobID), promoteTransactionID(jobID)
	if err := os.MkdirAll(filepath.Dir(effects.Dir), 0o700); err != nil {
		return nil, fmt.Errorf("create promote transaction directory: %w", err)
	}
	txn, err := checkpoint.BeginTransaction(effects)
	if err != nil {
		return nil, err
	}
	return &promoteTransaction{txn: txn, plans: append([]promoteMutation(nil), plans...)}, nil
}

// promoteTransactionID binds a transaction to its job, whatever characters a job ID holds.
func promoteTransactionID(jobID string) string { return "job-" + hex.EncodeToString([]byte(jobID)) }

func jobIDFromPromoteTransactionID(id string) (string, bool) {
	if len(id) < 4 || id[:4] != "job-" {
		return "", false
	}
	raw, err := hex.DecodeString(id[4:])
	return string(raw), err == nil
}

func (tx *promoteTransaction) apply(ctx context.Context, promote PromoteRoots, task *api.WorkerTask) error {
	for _, plan := range tx.plans {
		if err := context.Cause(ctx); err != nil {
			return errors.Join(err, tx.txn.Rollback(context.WithoutCancel(ctx)))
		}
		if err := verifyPromoteOriginal(promote, task, plan); err != nil {
			return errors.Join(err, tx.txn.Rollback(context.WithoutCancel(ctx)))
		}
	}
	roots, changes, err := promoteChanges(promote, task, tx.plans)
	if err != nil {
		return errors.Join(err, tx.txn.Rollback(context.WithoutCancel(ctx)))
	}
	return tx.txn.ApplyChanges(ctx, roots, changes)
}

// promoteChanges states byte plans as the transaction's target changes.
func promoteChanges(promote PromoteRoots, task *api.WorkerTask, plans []promoteMutation) (map[string]string, []checkpoint.PlannedChange, error) {
	roots := map[string]string{}
	changes := make([]checkpoint.PlannedChange, 0, len(plans))
	for _, plan := range plans {
		path, rootDir, err := promote.qualifiedTarget(task, plan.path)
		if err != nil {
			return nil, nil, err
		}
		roots[path.RootID] = rootDir
		change := checkpoint.PlannedChange{Path: path, Action: checkpoint.ActionDelete}
		if plan.targetExists {
			change.Action, change.MergedBytes = checkpoint.ActionWriteRegular, plan.target
			if change.MergedBytes == nil {
				change.MergedBytes = []byte{}
			}
			change.Mode = 0o600
			if plan.originalExists {
				change.Mode = plan.originalMode.Perm()
			}
		}
		changes = append(changes, change)
	}
	return roots, changes, nil
}

// qualifiedTarget names a promote path as a transaction target in its source root.
func (p PromoteRoots) qualifiedTarget(task *api.WorkerTask, path string) (layeredfs.QualifiedPath, string, error) {
	primaryAbs, _, err := p.fileAbs(task, path)
	if err != nil {
		return layeredfs.QualifiedPath{}, "", err
	}
	rootDir, err := p.primaryRoot(task, primaryAbs)
	if err != nil {
		return layeredfs.QualifiedPath{}, "", err
	}
	absolute, err := filepath.Abs(primaryAbs)
	if err != nil {
		return layeredfs.QualifiedPath{}, "", err
	}
	rel, err := filepath.Rel(rootDir, absolute)
	if err != nil {
		return layeredfs.QualifiedPath{}, "", err
	}
	return layeredfs.QualifiedPath{RootID: "promote-" + contentSHA256([]byte(rootDir))[:12], Rel: rel}, rootDir, nil
}

func verifyPromoteOriginal(promote PromoteRoots, task *api.WorkerTask, plan promoteMutation) error {
	primaryAbs, _, err := promote.fileAbs(task, plan.path)
	if err != nil {
		return err
	}
	content, exists, mode, err := readOptionalPromoteFile(primaryAbs)
	if err != nil {
		return err
	}
	if !promoteStateMatches(exists, mode, content, plan.originalExists, plan.originalMode, contentSHA256(plan.original)) {
		return fmt.Errorf("primary changed while promoting %q", plan.path)
	}
	return nil
}

func (tx *promoteTransaction) rollback(ctx context.Context) error { return tx.txn.Rollback(ctx) }

func (tx *promoteTransaction) close() error { return tx.txn.Commit() }

// recoverPromoteTransaction settles what a crash left for this job: a journal the shipped build wrote,
// and the current transaction. Each restores the original target.
func recoverPromoteTransaction(ctx context.Context, dataDir, jobID string, promote PromoteRoots, task *api.WorkerTask) (bool, error) {
	shipped, err := recoverShippedPromoteJournal(dataDir, jobID, promote, task)
	if err != nil {
		return shipped, err
	}
	found, err := checkpoint.RecoverTransaction(ctx, checkpoint.TransactionConfig{Dir: promoteTransactionDir(dataDir, jobID)})
	return shipped || found, err
}

func promoteStateMatches(exists bool, mode os.FileMode, content []byte, wantExists bool, wantMode os.FileMode, wantSHA string) bool {
	if exists != wantExists {
		return false
	}
	if !exists {
		return true
	}
	return mode.Perm() == wantMode.Perm() && contentSHA256(content) == wantSHA
}

func contentSHA256(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}
