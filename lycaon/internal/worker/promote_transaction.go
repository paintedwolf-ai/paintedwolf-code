package worker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fssync"
	"github.com/lycaon/lycaon/pkg/api"
)

const promoteTransactionVersion = 1

type promoteTransactionJournal struct {
	Version   int                      `json:"version"`
	JobID     string                   `json:"job_id"`
	Mutations []promoteJournalMutation `json:"mutations"`
}

type promoteJournalMutation struct {
	Path           string `json:"path"`
	Original       []byte `json:"original,omitempty"`
	OriginalExists bool   `json:"original_exists"`
	OriginalMode   uint32 `json:"original_mode,omitempty"`
	TargetSHA256   string `json:"target_sha256,omitempty"`
	TargetExists   bool   `json:"target_exists"`
	TargetMode     uint32 `json:"target_mode,omitempty"`
}

type promoteTransaction struct {
	path    string
	journal promoteTransactionJournal
	plans   []promoteMutation
}

type promoteTransactionUnstartedError struct{ cause error }

func (e *promoteTransactionUnstartedError) Error() string { return e.cause.Error() }
func (e *promoteTransactionUnstartedError) Unwrap() error { return e.cause }

func beginPromoteTransaction(dataDir, jobID string, plans []promoteMutation) (*promoteTransaction, error) {
	journal := promoteTransactionJournal{
		Version:   promoteTransactionVersion,
		JobID:     jobID,
		Mutations: make([]promoteJournalMutation, 0, len(plans)),
	}
	for _, plan := range plans {
		targetMode := os.FileMode(0)
		if plan.targetExists {
			if plan.originalExists {
				targetMode = plan.originalMode
			} else {
				targetMode = 0o600
			}
		}
		journal.Mutations = append(journal.Mutations, promoteJournalMutation{
			Path:           plan.path,
			Original:       append([]byte(nil), plan.original...),
			OriginalExists: plan.originalExists,
			OriginalMode:   uint32(plan.originalMode.Perm()),
			TargetSHA256:   contentSHA256(plan.target),
			TargetExists:   plan.targetExists,
			TargetMode:     uint32(targetMode.Perm()),
		})
	}
	tx := &promoteTransaction{
		path:    promoteTransactionPath(dataDir, jobID),
		journal: journal,
		plans:   append([]promoteMutation(nil), plans...),
	}
	if err := writePromoteTransaction(tx.path, journal); err != nil {
		return nil, err
	}
	return tx, nil
}

func (tx *promoteTransaction) apply(ctx context.Context, promote PromoteRoots, task *api.WorkerTask) error {
	for _, plan := range tx.plans {
		if err := context.Cause(ctx); err != nil {
			return err
		}
		if err := verifyPromoteOriginal(promote, task, plan); err != nil {
			return &promoteTransactionUnstartedError{cause: err}
		}
	}
	for _, plan := range tx.plans {
		if err := context.Cause(ctx); err != nil {
			return err
		}
		if err := applyTargetPromoteMutation(promote, task, plan); err != nil {
			return err
		}
	}
	return context.Cause(ctx)
}

func applyTargetPromoteMutation(promote PromoteRoots, task *api.WorkerTask, plan promoteMutation) error {
	verify := func(target fseffect.Target) error {
		return verifyPromoteTarget(target, plan.originalExists, plan.originalMode, contentSHA256(plan.original), plan.path)
	}
	if !plan.targetExists {
		return promote.dropPrimary(task, plan.path, verify)
	}
	return promote.writePrimaryBytes(task, plan.path, plan.target, nil, verify)
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

func verifyPromoteTarget(target fseffect.Target, wantExists bool, wantMode os.FileMode, wantSHA, path string) error {
	if !wantExists {
		if _, err := target.Lstat(); os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("primary changed while promoting %q", path)
	}
	f, err := target.Open()
	if err != nil {
		return fmt.Errorf("primary changed while promoting %q", path)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != wantMode.Perm() {
		return fmt.Errorf("primary changed while promoting %q", path)
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil || hex.EncodeToString(h.Sum(nil)) != wantSHA {
		return fmt.Errorf("primary changed while promoting %q", path)
	}
	return nil
}

func (tx *promoteTransaction) rollback(promote PromoteRoots, task *api.WorkerTask) error {
	return rollbackPromoteJournal(promote, task, tx.journal)
}

func (tx *promoteTransaction) close() error {
	return removePromoteTransaction(tx.path)
}

func recoverPromoteTransaction(dataDir, jobID string, promote PromoteRoots, task *api.WorkerTask) (bool, error) {
	path := promoteTransactionPath(dataDir, jobID)
	journal, found, err := readPromoteTransaction(path)
	if err != nil {
		return false, err
	}
	if !found {
		return false, nil
	}
	if journal.Version != promoteTransactionVersion || journal.JobID != jobID {
		return true, fmt.Errorf("promote transaction identity mismatch for %q", jobID)
	}
	if err := rollbackPromoteJournal(promote, task, journal); err != nil {
		return true, err
	}
	return true, nil
}

func rollbackPromoteJournal(promote PromoteRoots, task *api.WorkerTask, journal promoteTransactionJournal) error {
	type restore struct {
		plan promoteJournalMutation
	}
	restores := make([]restore, 0, len(journal.Mutations))
	for _, mutation := range journal.Mutations {
		primaryAbs, _, err := promote.fileAbs(task, mutation.Path)
		if err != nil {
			return err
		}
		current, exists, mode, err := readOptionalPromoteFile(primaryAbs)
		if err != nil {
			return err
		}
		if promoteStateMatches(exists, mode, current, mutation.OriginalExists, os.FileMode(mutation.OriginalMode), contentSHA256(mutation.Original)) {
			continue
		}
		if !promoteStateMatches(exists, mode, current, mutation.TargetExists, os.FileMode(mutation.TargetMode), mutation.TargetSHA256) {
			return fmt.Errorf("cannot recover promote transaction: %q diverged after interruption", mutation.Path)
		}
		restores = append(restores, restore{plan: mutation})
	}

	var errs []error
	for i := len(restores) - 1; i >= 0; i-- {
		mutation := restores[i].plan
		plan := promoteMutation{
			path:           mutation.Path,
			original:       mutation.Original,
			originalExists: mutation.OriginalExists,
			originalMode:   os.FileMode(mutation.OriginalMode),
		}
		verify := func(target fseffect.Target) error {
			return verifyPromoteTarget(target, mutation.TargetExists, os.FileMode(mutation.TargetMode), mutation.TargetSHA256, mutation.Path)
		}
		if err := applyOriginalPromoteMutation(promote, task, plan, verify); err != nil {
			errs = append(errs, fmt.Errorf("restore %s: %w", mutation.Path, err))
		}
	}
	return errors.Join(errs...)
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

func promoteTransactionPath(dataDir, jobID string) string {
	digest := sha256.Sum256([]byte(jobID))
	return filepath.Join(dataDir, "promote-transactions", hex.EncodeToString(digest[:])+".json")
}

func writePromoteTransaction(path string, journal promoteTransactionJournal) error {
	raw, err := json.Marshal(journal)
	if err != nil {
		return fmt.Errorf("encode promote transaction: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create promote transaction directory: %w", err)
	}
	if err := syncPromoteDirectory(filepath.Dir(dir)); err != nil {
		return err
	}
	_, err = fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.PathLocation(path),
		Source:   bytes.NewReader(raw), Mode: 0o600, DirMode: 0o700,
	})
	if err != nil {
		return fmt.Errorf("persist promote transaction: %w", err)
	}
	return nil
}

func readPromoteTransaction(path string) (promoteTransactionJournal, bool, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return promoteTransactionJournal{}, false, nil
	}
	if err != nil {
		return promoteTransactionJournal{}, false, fmt.Errorf("read promote transaction: %w", err)
	}
	var journal promoteTransactionJournal
	if err := json.Unmarshal(raw, &journal); err != nil {
		return promoteTransactionJournal{}, false, fmt.Errorf("decode promote transaction: %w", err)
	}
	return journal, true, nil
}

func removePromoteTransaction(path string) error {
	err := os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("remove promote transaction: %w", err)
	}
	return syncPromoteDirectory(filepath.Dir(path))
}

func syncPromoteDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open promote transaction directory: %w", err)
	}
	defer func() { _ = dir.Close() }()
	if err := fssync.File(dir); err != nil {
		return fmt.Errorf("sync promote transaction directory: %w", err)
	}
	return nil
}

func contentSHA256(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}
