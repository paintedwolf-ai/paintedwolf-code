package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/pkg/api"
)

type promoteTransaction struct {
	path    string
	journal promoteTransactionJournal
	plans   []promoteMutation
	effects promoteTransactionEffects
}

type promoteStep string

const (
	promoteStepPreparedDurable  promoteStep = "prepared-durable"
	promoteStepStaleCleared     promoteStep = "stale-cleared"
	promoteStepOriginalRetained promoteStep = "original-retained"
	promoteStepApplyingDurable  promoteStep = "applying-durable"
	promoteStepPrimaryMutated   promoteStep = "primary-mutated"
)

// promoteTransactionEffects holds the seams a crash-ordering contract observes.
type promoteTransactionEffects struct {
	reached  func(step promoteStep, path string) error
	hardLink func(fseffect.HardLinkRequest) error
}

func (e promoteTransactionEffects) reach(step promoteStep, path string) error {
	if e.reached == nil {
		return nil
	}
	return e.reached(step, path)
}

func (e promoteTransactionEffects) link(req fseffect.HardLinkRequest) error {
	if e.hardLink == nil {
		return fseffect.HardLink(req)
	}
	return e.hardLink(req)
}

type promoteTransactionUnstartedError struct{ cause error }

func (e *promoteTransactionUnstartedError) Error() string { return e.cause.Error() }
func (e *promoteTransactionUnstartedError) Unwrap() error { return e.cause }

func beginPromoteTransaction(dataDir, jobID string, plans []promoteMutation) (*promoteTransaction, error) {
	return beginPromoteTransactionWith(dataDir, jobID, plans, promoteTransactionEffects{})
}

func beginPromoteTransactionWith(dataDir, jobID string, plans []promoteMutation, effects promoteTransactionEffects) (*promoteTransaction, error) {
	journal := promoteTransactionJournal{
		Version:   promoteTransactionVersion,
		JobID:     jobID,
		Phase:     promotePhasePrepared,
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
	tx := &promoteTransaction{path: promoteTransactionPath(dataDir, jobID), journal: journal, plans: append([]promoteMutation(nil), plans...), effects: effects}
	if err := writePromoteTransaction(tx.path, journal); err != nil {
		return nil, err
	}
	if err := effects.reach(promoteStepPreparedDurable, ""); err != nil {
		return nil, err
	}
	// Only now is a predecessor's rollback authority gone. A prepared journal gives
	// slots no meaning, so a removal lost to a crash cannot be mistaken for retention.
	if err := os.RemoveAll(promoteTransactionRetainedDir(tx.path)); err != nil {
		return nil, fmt.Errorf("clear retained promote originals: %w", err)
	}
	if err := effects.reach(promoteStepStaleCleared, ""); err != nil {
		return nil, err
	}
	return tx, nil
}

func (tx *promoteTransaction) apply(ctx context.Context, promote PromoteRoots, task *api.WorkerTask) error {
	for _, plan := range tx.plans {
		if err := context.Cause(ctx); err != nil {
			return &promoteTransactionUnstartedError{cause: err}
		}
		if err := verifyPromoteOriginal(promote, task, plan); err != nil {
			return &promoteTransactionUnstartedError{cause: err}
		}
	}
	retained, err := retainPromoteOriginals(tx.path, promote, task, tx.plans, tx.effects)
	if err != nil {
		return &promoteTransactionUnstartedError{cause: err}
	}
	tx.journal.Phase = promotePhaseApplying
	for i := range tx.journal.Mutations {
		mutation := &tx.journal.Mutations[i]
		mutation.OriginalRetention, mutation.OriginalIdentity = retained[mutation.Path].retention, retained[mutation.Path].identity
	}
	// Replace syncs the file, renames, then syncs the parent, so the phase is
	// durable when it returns and every mutation below has a rollback authority.
	if err := writePromoteTransaction(tx.path, tx.journal); err != nil {
		return &promoteTransactionUnstartedError{cause: err}
	}
	if err := tx.effects.reach(promoteStepApplyingDurable, ""); err != nil {
		return err
	}
	for _, plan := range tx.plans {
		if err := context.Cause(ctx); err != nil {
			return err
		}
		if err := applyTargetPromoteMutation(promote, task, plan); err != nil {
			return err
		}
		if err := tx.effects.reach(promoteStepPrimaryMutated, plan.path); err != nil {
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
	return rollbackPromoteJournalAt(tx.path, promote, task, tx.journal)
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
		// Close removes the journal first, so these slots carry no authority.
		if err := os.RemoveAll(promoteTransactionRetainedDir(path)); err != nil {
			return false, fmt.Errorf("clear retained promote originals: %w", err)
		}
		return false, nil
	}
	if err := validatePromoteJournal(journal, jobID); err != nil {
		return true, err
	}
	if err := rollbackPromoteJournalAt(path, promote, task, journal); err != nil {
		return true, err
	}
	return true, nil
}

func rollbackPromoteJournalAt(txPath string, promote PromoteRoots, task *api.WorkerTask, journal promoteTransactionJournal) error {
	// The applying phase is durable before any mutation, so its absence proves
	// the primaries were never touched and retained slots are not needed.
	if journal.Phase == promotePhasePrepared {
		return nil
	}
	restores := make([]promoteJournalMutation, 0, len(journal.Mutations))
	for _, mutation := range journal.Mutations {
		if err := validateRetainedPromoteOriginal(txPath, mutation); err != nil {
			return err
		}
		primaryAbs, _, err := promote.fileAbs(task, mutation.Path)
		if err != nil {
			return err
		}
		current, exists, mode, err := readOptionalPromoteFile(primaryAbs)
		if err != nil {
			return err
		}
		if promoteStateMatches(exists, mode, current, mutation.OriginalExists, os.FileMode(mutation.OriginalMode), contentSHA256(mutation.Original)) &&
			promoteInodeMatchesRetained(txPath, primaryAbs, mutation) {
			continue
		}
		if !promoteStateMatches(exists, mode, current, mutation.TargetExists, os.FileMode(mutation.TargetMode), mutation.TargetSHA256) &&
			!promoteStateMatches(exists, mode, current, mutation.OriginalExists, os.FileMode(mutation.OriginalMode), contentSHA256(mutation.Original)) {
			return fmt.Errorf("cannot recover promote transaction: %q diverged after interruption", mutation.Path)
		}
		restores = append(restores, mutation)
	}

	var errs []error
	for i := len(restores) - 1; i >= 0; i-- {
		mutation := restores[i]
		verify := func(target fseffect.Target) error {
			if err := verifyPromoteTarget(target, mutation.TargetExists, os.FileMode(mutation.TargetMode), mutation.TargetSHA256, mutation.Path); err == nil {
				return nil
			}
			return verifyPromoteTarget(target, mutation.OriginalExists, os.FileMode(mutation.OriginalMode), contentSHA256(mutation.Original), mutation.Path)
		}
		if err := restorePromoteOriginalMutation(txPath, promote, task, mutation, verify); err != nil {
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

func contentSHA256(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}
