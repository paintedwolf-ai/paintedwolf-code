package worker

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fssync"
)

const (
	// Version 1 is the shipped shape: no phase and no retained originals.
	promoteTransactionVersionShipped = 1
	promoteTransactionVersion        = 2

	// promotePhasePrepared is durable before retention begins and proves no
	// primary mutation has started. promotePhaseApplying is durable, after every
	// retained original, before the first primary mutation.
	promotePhasePrepared = "prepared"
	promotePhaseApplying = "applying"

	promoteRetentionInode   = "inode"
	promoteRetentionContent = "content"
)

type promoteTransactionJournal struct {
	Version   int                      `json:"version"`
	JobID     string                   `json:"job_id"`
	Phase     string                   `json:"phase,omitempty"`
	Mutations []promoteJournalMutation `json:"mutations"`
}

type promoteJournalMutation struct {
	Path              string `json:"path"`
	Original          []byte `json:"original,omitempty"`
	OriginalExists    bool   `json:"original_exists"`
	OriginalMode      uint32 `json:"original_mode,omitempty"`
	OriginalRetention string `json:"original_retention,omitempty"`
	OriginalIdentity  string `json:"original_identity,omitempty"`
	TargetSHA256      string `json:"target_sha256,omitempty"`
	TargetExists      bool   `json:"target_exists"`
	TargetMode        uint32 `json:"target_mode,omitempty"`
}

// validatePromoteJournal refuses any shape no build writes, without touching a primary.
func validatePromoteJournal(journal promoteTransactionJournal, jobID string) error {
	if journal.JobID != jobID {
		return fmt.Errorf("promote transaction identity mismatch for %q", jobID)
	}
	switch journal.Version {
	case promoteTransactionVersionShipped:
		if journal.Phase != "" {
			return fmt.Errorf("promote transaction version %d has phase %q", journal.Version, journal.Phase)
		}
	case promoteTransactionVersion:
		if journal.Phase != promotePhasePrepared && journal.Phase != promotePhaseApplying {
			return fmt.Errorf("promote transaction has unknown phase %q", journal.Phase)
		}
	default:
		return fmt.Errorf("promote transaction version %d is unsupported", journal.Version)
	}
	for _, mutation := range journal.Mutations {
		retention, identity := mutation.OriginalRetention, mutation.OriginalIdentity
		var valid bool
		switch {
		case journal.Phase != promotePhaseApplying || !mutation.OriginalExists:
			valid = retention == "" && identity == ""
		case retention == promoteRetentionInode:
			valid = identity != ""
		case retention == promoteRetentionContent:
			valid = identity == ""
		}
		if !valid {
			return fmt.Errorf("promote transaction has invalid retention %q for %q", retention, mutation.Path)
		}
	}
	return nil
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
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&journal); err != nil {
		return promoteTransactionJournal{}, false, fmt.Errorf("decode promote transaction: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return promoteTransactionJournal{}, false, fmt.Errorf("decode promote transaction: trailing data")
	}
	return journal, true, nil
}

// removePromoteTransaction drops the journal first: slots without a journal are
// inert, while an applying journal without its slots could never be recovered.
func removePromoteTransaction(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove promote transaction: %w", err)
	}
	dir := filepath.Dir(path)
	if err := syncPromoteDirectory(dir); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.RemoveAll(promoteTransactionRetainedDir(path)); err != nil {
		return fmt.Errorf("clear retained promote originals: %w", err)
	}
	return nil
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
