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

	"github.com/lycaon/lycaon/internal/fseffect"
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
	if phase == "" {
		phase = PhaseActiveMutation
	}
	return preserveInQuarantine(c, InterruptedWorkingRecord{
		WorkerID: workerID, ViewID: viewID, BaseCheckpointID: baseCheckpointID, InterruptedPhase: phase, Diagnostic: diag,
	}, func(staging string) (salvagedWork, error) {
		paths, inferredOp, inferredUnknown, copiedBytes, err := salvageBackingDir(backingDir, staging, knownPaths, op, unknownScope)
		return salvagedWork{paths: paths, op: inferredOp, unknownScope: inferredUnknown, bytes: copiedBytes}, err
	})
}

// salvagedWork is what filling a quarantine reports for its record and inventory entry.
type salvagedWork struct {
	paths        []layeredfs.QualifiedPath
	op           string
	unknownScope bool
	bytes        int64
}

// preserveInQuarantine fills the view's quarantine in staging, promotes it and writes its record.
func preserveInQuarantine(c *Coordinator, rec InterruptedWorkingRecord, fill func(staging string) (salvagedWork, error)) (*InterruptedWorkingRecord, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// One record per view makes a retried salvage find its finished work. The record lands
	// last, so a partial quarantine is never reported as preserved.
	id := fmt.Sprintf("intr-%s-%s", rec.WorkerID, rec.ViewID)
	if existing, ok, err := loadInterruptedRecordLocked(c, id); err != nil || ok {
		return existing, err
	}
	quarantineDir := filepath.Join(c.interruptedDir, id)
	staging := quarantineDir + ".staging"
	if err := os.RemoveAll(staging); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(staging, 0o700); err != nil {
		return nil, err
	}
	work, err := fill(staging)
	if err != nil {
		_ = os.RemoveAll(staging)
		return nil, fmt.Errorf("recovery: salvage working storage of %s: %w", rec.ViewID, err)
	}
	if err := syncStagedTree(staging); err != nil {
		_ = os.RemoveAll(staging)
		return nil, err
	}
	if err := os.RemoveAll(quarantineDir); err != nil {
		return nil, err
	}
	if err := os.Rename(staging, quarantineDir); err != nil {
		_ = os.RemoveAll(staging)
		return nil, err
	}
	rec.FormatVersion, rec.ID, rec.QuarantineDir, rec.CreatedAt = FormatVersion, id, quarantineDir, time.Now().UTC()
	rec.InterruptedOp, rec.AffectedPaths, rec.UnknownScope = work.op, work.paths, work.unknownScope
	if err := upsertInventoryLocked(c, InventoryEntry{
		ID:           "inv-interrupted-" + id,
		Bucket:       BucketInterruptedWorkingData,
		WorkerID:     rec.WorkerID,
		ViewID:       rec.ViewID,
		CheckpointID: rec.BaseCheckpointID,
		Path:         quarantineDir,
		Bytes:        work.bytes,
		PinnedBy:     []string{rec.WorkerID},
	}); err != nil {
		return nil, err
	}
	if err := writeAtomicJSON(c, c.interruptedDir, id+".json", rec); err != nil {
		return nil, err
	}
	return &rec, nil
}

// syncStagedTree flushes every staged directory, so what the record will name survives the
// source being deleted. A staged file is flushed by its copy or is the worker's own inode.
func syncStagedTree(staging string) error {
	root, err := os.OpenRoot(staging)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return filepath.WalkDir(staging, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || !entry.IsDir() {
			return walkErr
		}
		rel, err := filepath.Rel(staging, path)
		if err != nil {
			return err
		}
		return fseffect.SyncDirectory(root, rel)
	})
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
