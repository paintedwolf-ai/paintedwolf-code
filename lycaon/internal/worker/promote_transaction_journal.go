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
	"strings"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/fssync"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
	"github.com/lycaon/lycaon/pkg/api"
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

	promoteTransactionsDirName = "promote-transactions"
	promoteJournalSuffix       = ".json"
	promoteRetainedSuffix      = ".retained"

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
	return filepath.Join(dataDir, promoteTransactionsDirName, hex.EncodeToString(digest[:])+promoteJournalSuffix)
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

func promoteTransactionRetainedDir(txPath string) string {
	if strings.TrimSpace(txPath) == "" {
		return ""
	}
	return strings.TrimSuffix(txPath, promoteJournalSuffix) + promoteRetainedSuffix
}

func promoteRetainedSlot(relPath string) string {
	return "slot-" + contentSHA256([]byte(relPath))
}

type retainedPromoteOriginal struct {
	retention string
	identity  string
}

// retainPromoteOriginals links each original into host storage. Each link is
// durable before the next, and none has any meaning until the applying phase names it.
func retainPromoteOriginals(txPath string, promote PromoteRoots, task *api.WorkerTask, plans []promoteMutation, effects promoteTransactionEffects) (map[string]retainedPromoteOriginal, error) {
	retainedDir := promoteTransactionRetainedDir(txPath)
	retained := make(map[string]retainedPromoteOriginal, len(plans))
	for _, plan := range plans {
		if !plan.originalExists {
			continue
		}
		original, err := retainPromoteOriginal(retainedDir, promote, task, plan, effects)
		if err != nil {
			return nil, err
		}
		retained[plan.path] = original
		if err := effects.reach(promoteStepOriginalRetained, plan.path); err != nil {
			return nil, err
		}
	}
	return retained, nil
}

func retainPromoteOriginal(retainedDir string, promote PromoteRoots, task *api.WorkerTask, plan promoteMutation, effects promoteTransactionEffects) (retainedPromoteOriginal, error) {
	primaryAbs, _, err := promote.fileAbs(task, plan.path)
	if err != nil {
		return retainedPromoteOriginal{}, err
	}
	primaryRoot, err := promote.primaryRoot(task, primaryAbs)
	if err != nil {
		return retainedPromoteOriginal{}, err
	}
	primaryRel, err := filepath.Rel(primaryRoot, primaryAbs)
	if err != nil {
		return retainedPromoteOriginal{}, err
	}
	slot := promoteRetainedSlot(plan.path)
	err = effects.link(fseffect.HardLinkRequest{
		Source:   fseffect.Location{Root: primaryRoot, Rel: primaryRel},
		Location: fseffect.Location{Root: filepath.Dir(retainedDir), Rel: filepath.Join(filepath.Base(retainedDir), slot)},
		DirMode:  0o700,
		Replace:  true,
	})
	if fseffect.HardLinkUnavailable(err) {
		return promoteOriginalWithoutInode(primaryAbs, plan, err)
	}
	if err != nil {
		return retainedPromoteOriginal{}, err
	}
	slotPath := filepath.Join(retainedDir, slot)
	content, exists, mode, err := readOptionalPromoteFile(slotPath)
	if err != nil {
		return retainedPromoteOriginal{}, err
	}
	if !promoteStateMatches(exists, mode, content, plan.originalExists, plan.originalMode, contentSHA256(plan.original)) {
		return retainedPromoteOriginal{}, fmt.Errorf("primary changed while promoting %q", plan.path)
	}
	origID, err := fspath.EntryIdentity(primaryAbs)
	if err != nil {
		return retainedPromoteOriginal{}, err
	}
	slotID, err := fspath.EntryIdentity(slotPath)
	if err != nil {
		return retainedPromoteOriginal{}, err
	}
	if origID == "" || origID != slotID {
		return retainedPromoteOriginal{}, fmt.Errorf("primary changed while promoting %q", plan.path)
	}
	return retainedPromoteOriginal{retention: promoteRetentionInode, identity: slotID}, nil
}

// promoteOriginalWithoutInode serves a project on another volume from host data.
// The journal already holds the bytes; only aliases need the inode itself, so
// those refuse before any mutation rather than sever the link on rollback.
func promoteOriginalWithoutInode(primaryAbs string, plan promoteMutation, cause error) (retainedPromoteOriginal, error) {
	links, err := checkpoint.TargetFileLinks(primaryAbs)
	if err != nil {
		return retainedPromoteOriginal{}, err
	}
	if links > 1 {
		return retainedPromoteOriginal{}, fmt.Errorf("cannot retain hard-linked original %q: %w", plan.path, cause)
	}
	return retainedPromoteOriginal{retention: promoteRetentionContent}, nil
}

func validateRetainedPromoteOriginal(txPath string, mutation promoteJournalMutation) error {
	if mutation.OriginalRetention != promoteRetentionInode {
		return nil
	}
	slotPath := filepath.Join(promoteTransactionRetainedDir(txPath), promoteRetainedSlot(mutation.Path))
	content, exists, mode, err := readOptionalPromoteFile(slotPath)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("cannot recover promote transaction: missing retained original for %q", mutation.Path)
	}
	if !promoteStateMatches(exists, mode, content, mutation.OriginalExists, os.FileMode(mutation.OriginalMode), contentSHA256(mutation.Original)) {
		return fmt.Errorf("cannot recover promote transaction: %q original inode diverged", mutation.Path)
	}
	slotID, err := fspath.EntryIdentity(slotPath)
	if err != nil || !fspath.SameEntryAcrossRestart(mutation.OriginalIdentity, slotID) {
		return fmt.Errorf("cannot recover promote transaction: %q original inode diverged", mutation.Path)
	}
	return nil
}

func promoteInodeMatchesRetained(txPath, primaryAbs string, mutation promoteJournalMutation) bool {
	if mutation.OriginalRetention != promoteRetentionInode {
		return true
	}
	primary, err := os.Lstat(primaryAbs)
	if err != nil {
		return false
	}
	slot, err := os.Lstat(filepath.Join(promoteTransactionRetainedDir(txPath), promoteRetainedSlot(mutation.Path)))
	return err == nil && os.SameFile(primary, slot)
}

func restorePromoteOriginalMutation(txPath string, promote PromoteRoots, task *api.WorkerTask, mutation promoteJournalMutation, verify func(fseffect.Target) error) error {
	plan := promoteMutation{path: mutation.Path, original: mutation.Original, originalExists: mutation.OriginalExists, originalMode: os.FileMode(mutation.OriginalMode)}
	if mutation.OriginalRetention != promoteRetentionInode {
		return applyOriginalPromoteMutation(promote, task, plan, verify)
	}
	primaryAbs, _, err := promote.fileAbs(task, plan.path)
	if err != nil {
		return err
	}
	primaryRoot, err := promote.primaryRoot(task, primaryAbs)
	if err != nil {
		return err
	}
	primaryRel, err := filepath.Rel(primaryRoot, primaryAbs)
	if err != nil {
		return err
	}
	if err := fseffect.HardLink(fseffect.HardLinkRequest{
		Source:       fseffect.Location{Root: promoteTransactionRetainedDir(txPath), Rel: promoteRetainedSlot(plan.path)},
		Location:     fseffect.Location{Root: primaryRoot, Rel: primaryRel},
		DirMode:      0o750,
		Replace:      true,
		BeforeCommit: verify,
	}); err != nil {
		return err
	}
	_, err = fseffect.UpdateMode(fseffect.ModeUpdateRequest{
		Location: fseffect.Location{Root: primaryRoot, Rel: primaryRel},
		Update:   func(os.FileMode) os.FileMode { return plan.originalMode.Perm() },
	})
	return err
}
