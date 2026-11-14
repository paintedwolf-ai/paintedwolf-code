package worker

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/pkg/api"
)

const (
	// shippedPromoteJournalVersion is the shape a released build may have left behind.
	shippedPromoteJournalVersion = 1

	promoteTransactionsDirName = "promote-transactions"
	shippedPromoteJournal      = ".json"
	promoteTransactionSuffix   = ".tx"
)

type shippedPromoteTransaction struct {
	Version   int                     `json:"version"`
	JobID     string                  `json:"job_id"`
	Mutations []shippedPromoteRestore `json:"mutations"`
}

type shippedPromoteRestore struct {
	Path           string `json:"path"`
	Original       []byte `json:"original,omitempty"`
	OriginalExists bool   `json:"original_exists"`
	OriginalMode   uint32 `json:"original_mode,omitempty"`
	TargetSHA256   string `json:"target_sha256,omitempty"`
	TargetExists   bool   `json:"target_exists"`
	TargetMode     uint32 `json:"target_mode,omitempty"`
}

func promoteTransactionKey(dataDir, jobID string) string {
	digest := sha256.Sum256([]byte(jobID))
	return filepath.Join(dataDir, promoteTransactionsDirName, hex.EncodeToString(digest[:]))
}

func promoteTransactionDir(dataDir, jobID string) string {
	return promoteTransactionKey(dataDir, jobID) + promoteTransactionSuffix
}

func shippedPromoteJournalPath(dataDir, jobID string) string {
	return promoteTransactionKey(dataDir, jobID) + shippedPromoteJournal
}

func readShippedPromoteJournal(path string) (shippedPromoteTransaction, bool, error) {
	var journal shippedPromoteTransaction
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return journal, false, nil
	}
	if err != nil {
		return journal, false, fmt.Errorf("read promote transaction: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&journal); err != nil {
		return journal, false, fmt.Errorf("decode promote transaction: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return journal, false, errors.New("decode promote transaction: trailing data")
	}
	if journal.Version != shippedPromoteJournalVersion {
		return journal, false, fmt.Errorf("promote transaction version %d is unsupported", journal.Version)
	}
	return journal, true, nil
}

// recoverShippedPromoteJournal restores a promotion the released build was applying at upgrade. Its
// journal holds the original bytes inline. Any other shape refuses without touching a primary.
func recoverShippedPromoteJournal(dataDir, jobID string, promote PromoteRoots, task *api.WorkerTask) (bool, error) {
	path := shippedPromoteJournalPath(dataDir, jobID)
	journal, found, err := readShippedPromoteJournal(path)
	if err != nil || !found {
		return false, err
	}
	if journal.JobID != jobID {
		return true, fmt.Errorf("promote transaction identity mismatch for %q", jobID)
	}
	restores := make([]shippedPromoteRestore, 0, len(journal.Mutations))
	for _, mutation := range journal.Mutations {
		primaryAbs, _, err := promote.fileAbs(task, mutation.Path)
		if err != nil {
			return true, err
		}
		current, exists, mode, err := readOptionalPromoteFile(primaryAbs)
		if err != nil {
			return true, err
		}
		if promoteStateMatches(exists, mode, current, mutation.OriginalExists, os.FileMode(mutation.OriginalMode), contentSHA256(mutation.Original)) {
			continue
		}
		if !promoteStateMatches(exists, mode, current, mutation.TargetExists, os.FileMode(mutation.TargetMode), mutation.TargetSHA256) {
			return true, fmt.Errorf("cannot recover promote transaction: %q diverged after interruption", mutation.Path)
		}
		restores = append(restores, mutation)
	}
	var errs []error
	for i := len(restores) - 1; i >= 0; i-- {
		mutation := restores[i]
		verify := func(target fseffect.Target) error {
			return verifyPromoteTarget(target, mutation.TargetExists, os.FileMode(mutation.TargetMode), mutation.TargetSHA256, mutation.Path)
		}
		plan := promoteMutation{path: mutation.Path, original: mutation.Original, originalExists: mutation.OriginalExists, originalMode: os.FileMode(mutation.OriginalMode)}
		if err := applyOriginalPromoteMutation(promote, task, plan, verify); err != nil {
			errs = append(errs, fmt.Errorf("restore %s: %w", mutation.Path, err))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return true, err
	}
	return true, removeShippedPromoteJournal(path)
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

func removeShippedPromoteJournal(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove promote transaction: %w", err)
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("open promote transaction directory: %w", err)
	}
	defer func() { _ = dir.Close() }()
	return syncPromoteTransactionDir(dir)
}
