package recovery

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/fssync"
	"github.com/lycaon/lycaon/internal/layeredfs"
)

// PreserveInterruptedWorkingStorage salvages incomplete working state into a quarantine directory
// and records an InterruptedWorkingRecord and BucketInterruptedWorkingData inventory entry.
func (c *Coordinator) PreserveInterruptedWorkingStorage(workerID, viewID, baseCheckpointID, backingDir string, phase TransitionPhase, op string, knownPaths []layeredfs.QualifiedPath, unknownScope bool, diag string) (*InterruptedWorkingRecord, error) {
	return preserveInterruptedWorkingStorage(c, workerID, viewID, baseCheckpointID, backingDir, phase, op, knownPaths, unknownScope, diag)
}

func preserveInterruptedWorkingStorage(
	c *Coordinator,
	workerID, viewID, baseCheckpointID, backingDir string,
	phase TransitionPhase,
	op string,
	knownPaths []layeredfs.QualifiedPath,
	unknownScope bool,
	diag string,
) (*InterruptedWorkingRecord, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// One record per view makes a retried salvage find its finished work. The record lands
	// last, so a partial quarantine is never reported as preserved.
	id := fmt.Sprintf("intr-%s-%s", workerID, viewID)
	if existing, ok, err := loadInterruptedRecordLocked(c, id); err != nil || ok {
		return existing, err
	}
	quarantineDir := filepath.Join(c.interruptedDir, id)
	staging := quarantineDir + ".staging"
	if err := os.RemoveAll(staging); err != nil {
		return nil, err
	}
	paths, inferredOp, inferredUnknown, copiedBytes, err := salvageBackingDir(backingDir, staging, knownPaths, op, unknownScope)
	if err != nil {
		_ = os.RemoveAll(staging)
		return nil, fmt.Errorf("recovery: salvage working storage of %s: %w", viewID, err)
	}
	if err := os.RemoveAll(quarantineDir); err != nil {
		return nil, err
	}
	if err := os.Rename(staging, quarantineDir); err != nil {
		_ = os.RemoveAll(staging)
		return nil, err
	}
	if phase == "" {
		phase = PhaseActiveMutation
	}
	rec := InterruptedWorkingRecord{
		FormatVersion:    FormatVersion,
		ID:               id,
		WorkerID:         workerID,
		ViewID:           viewID,
		BaseCheckpointID: baseCheckpointID,
		QuarantineDir:    quarantineDir,
		InterruptedPhase: phase,
		InterruptedOp:    inferredOp,
		AffectedPaths:    paths,
		UnknownScope:     inferredUnknown,
		Diagnostic:       diag,
		CreatedAt:        time.Now().UTC(),
	}
	if err := writeAtomicJSON(c, c.interruptedDir, id+".json", rec); err != nil {
		return nil, err
	}
	if err := upsertInventoryLocked(c, InventoryEntry{
		ID:           "inv-interrupted-" + id,
		Bucket:       BucketInterruptedWorkingData,
		WorkerID:     workerID,
		ViewID:       viewID,
		CheckpointID: baseCheckpointID,
		Path:         quarantineDir,
		Bytes:        copiedBytes,
		PinnedBy:     []string{workerID},
	}); err != nil {
		return nil, err
	}
	return &rec, nil
}

func salvageBackingDir(
	backingDir, quarantineDir string,
	knownPaths []layeredfs.QualifiedPath,
	op string,
	unknownScope bool,
) ([]layeredfs.QualifiedPath, string, bool, int64, error) {
	seen := make(map[layeredfs.QualifiedPath]struct{}, len(knownPaths))
	var out []layeredfs.QualifiedPath
	addPath := func(qp layeredfs.QualifiedPath) {
		if qp.RootID == "" || qp.Rel == "" {
			return
		}
		if _, ok := seen[qp]; !ok {
			seen[qp] = struct{}{}
			out = append(out, qp)
		}
	}
	for _, qp := range knownPaths {
		addPath(qp)
	}
	if strings.TrimSpace(backingDir) != "" {
		raw, err := os.ReadFile(filepath.Join(backingDir, "journal", "journal.json"))
		if err == nil {
			if snap, pErr := layeredfs.ParseJournalSnapshot(raw); pErr == nil {
				for _, jop := range snap.Operations {
					addPath(jop.Path)
					if jop.SourcePath != nil {
						addPath(*jop.SourcePath)
					}
					if op == "" {
						op = string(jop.Kind)
					}
				}
				for _, ch := range snap.Changes {
					addPath(ch.Path)
				}
			} else {
				unknownScope = true
			}
		} else if !errors.Is(err, fs.ErrNotExist) {
			unknownScope = true
		}
	}
	if err := os.MkdirAll(quarantineDir, 0o700); err != nil {
		return nil, "", false, 0, err
	}
	copiedBytes, err := copyWorkingTree(backingDir, filepath.Join(quarantineDir, "backing"))
	if err != nil {
		return nil, "", false, 0, err
	}
	if len(out) == 0 && (copiedBytes > 0 || unknownScope) {
		unknownScope = true
	}
	return out, op, unknownScope, copiedBytes, nil
}

func loadInterruptedRecordLocked(c *Coordinator, id string) (*InterruptedWorkingRecord, bool, error) {
	raw, err := os.ReadFile(filepath.Join(c.interruptedDir, id+".json"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var rec InterruptedWorkingRecord
	if err := decodeVersionedJSON(raw, &rec); err != nil {
		return nil, false, err
	}
	return &rec, true, nil
}

// copyWorkingTree fails on any entry it cannot preserve, so a full disk leaves the source
// in place and the salvage retryable instead of a quarantine missing content.
func copyWorkingTree(srcDir, dstDir string) (int64, error) {
	if strings.TrimSpace(srcDir) == "" {
		return 0, nil
	}
	if _, err := os.Lstat(srcDir); errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	var total int64
	err := filepath.WalkDir(srcDir, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(srcDir, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dstDir, rel)
		switch {
		case d.IsDir():
			return os.MkdirAll(target, 0o700)
		case d.Type()&fs.ModeSymlink != 0:
			link, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		case d.Type().IsRegular():
			n, err := copyWorkingFile(p, target)
			total += n
			return err
		}
		return nil
	})
	return total, err
}

func copyWorkingFile(src, dst string) (written int64, err error) {
	in, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, err
	}
	written, copyErr := io.Copy(out, in)
	syncErr := error(nil)
	if !fssync.Relaxed() {
		syncErr = out.Sync()
	}
	return written, errors.Join(copyErr, syncErr, out.Close())
}
