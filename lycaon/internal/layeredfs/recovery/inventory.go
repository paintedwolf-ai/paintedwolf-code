package recovery

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/layeredfs"
)

// RecordCheckpointLanded persists explicit ancestry and target digest facts when a checkpoint promotes to trunk.
func (c *Coordinator) RecordCheckpointLanded(receipt LandingReceipt) error {
	return recordCheckpointLanded(c, receipt)
}

func recordCheckpointLanded(c *Coordinator, receipt LandingReceipt) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if strings.TrimSpace(receipt.CheckpointID) == "" || strings.TrimSpace(receipt.WorkerID) == "" {
		return fmt.Errorf("%w: checkpoint_id and worker_id are required", ErrCorruptRecoveryMetadata)
	}
	receipt.FormatVersion = FormatVersion
	if receipt.LandedAt.IsZero() {
		receipt.LandedAt = time.Now().UTC()
	}
	return writeAtomicJSON(c, c.landingsDir, receipt.CheckpointID+".json", receipt)
}

// LoadLandingReceipt loads the recorded landing receipt for a checkpoint, if present.
func (c *Coordinator) LoadLandingReceipt(checkpointID string) (LandingReceipt, bool, error) {
	return loadLandingReceipt(c, checkpointID)
}

func loadLandingReceipt(c *Coordinator, checkpointID string) (LandingReceipt, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	raw, err := os.ReadFile(filepath.Join(c.landingsDir, checkpointID+".json"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return LandingReceipt{}, false, nil
		}
		return LandingReceipt{}, false, err
	}
	var receipt LandingReceipt
	if err := decodeVersionedJSON(raw, &receipt); err != nil {
		return LandingReceipt{}, false, err
	}
	if receipt.CheckpointID != checkpointID {
		return LandingReceipt{}, false, fmt.Errorf("%w: landing receipt id mismatch", ErrCorruptRecoveryMetadata)
	}
	return receipt, true, nil
}

// LoadInventory reads and validates the local-data inventory across all three buckets.
func (c *Coordinator) LoadInventory() (LocalDataInventory, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return loadInventoryLocked(c)
}

func loadInventoryLocked(c *Coordinator) (LocalDataInventory, error) {
	raw, err := os.ReadFile(c.inventoryPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return LocalDataInventory{FormatVersion: FormatVersion, UpdatedAt: time.Now().UTC()}, nil
		}
		return LocalDataInventory{}, err
	}
	var inv LocalDataInventory
	if err := decodeVersionedJSON(raw, &inv); err != nil {
		return LocalDataInventory{}, err
	}
	return inv, nil
}

// UpsertInventoryEntry inserts or updates one entry in the categorized local-data inventory.
func (c *Coordinator) UpsertInventoryEntry(entry InventoryEntry) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return upsertInventoryLocked(c, entry)
}

func upsertInventoryLocked(c *Coordinator, entry InventoryEntry) error {
	inv, err := loadInventoryLocked(c)
	if err != nil {
		return err
	}
	entry.UpdatedAt = time.Now().UTC()
	replaced := false
	for i := range inv.Entries {
		if inv.Entries[i].ID == entry.ID {
			inv.Entries[i] = entry
			replaced = true
			break
		}
	}
	if !replaced {
		inv.Entries = append(inv.Entries, entry)
	}
	inv.FormatVersion = FormatVersion
	inv.UpdatedAt = entry.UpdatedAt
	return writeAtomicJSON(c, c.cfg.RootDir, "inventory.json", inv)
}

// RemoveInventoryEntry removes an entry by ID from the local-data inventory.
func (c *Coordinator) RemoveInventoryEntry(id string) error {
	return removeInventoryEntry(c, id)
}

func removeInventoryEntry(c *Coordinator, id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	inv, err := loadInventoryLocked(c)
	if err != nil {
		return err
	}
	filtered := inv.Entries[:0]
	for _, e := range inv.Entries {
		if e.ID != id {
			filtered = append(filtered, e)
		}
	}
	inv.Entries = filtered
	inv.UpdatedAt = time.Now().UTC()
	return writeAtomicJSON(c, c.cfg.RootDir, "inventory.json", inv)
}

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

	id := fmt.Sprintf("intr-%s-%s-%d", workerID, viewID, c.seq.Add(1))
	quarantineDir := filepath.Join(c.interruptedDir, id)
	if err := os.MkdirAll(quarantineDir, 0o700); err != nil {
		return nil, err
	}

	paths, inferredOp, inferredUnknown, copiedBytes := salvageBackingDir(backingDir, quarantineDir, knownPaths, op, unknownScope)
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

// ListInterruptedRecords loads all salvaged InterruptedWorkingRecords for workerID.
func (c *Coordinator) ListInterruptedRecords(workerID string) ([]InterruptedWorkingRecord, error) {
	return listInterruptedRecords(c, workerID)
}

func listInterruptedRecords(c *Coordinator, workerID string) ([]InterruptedWorkingRecord, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entries, err := os.ReadDir(c.interruptedDir)
	if err != nil {
		return nil, err
	}
	var out []InterruptedWorkingRecord
	for _, d := range entries {
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(c.interruptedDir, d.Name()))
		if err != nil {
			return nil, err
		}
		var rec InterruptedWorkingRecord
		if err := decodeVersionedJSON(raw, &rec); err != nil {
			return nil, err
		}
		if workerID == "" || rec.WorkerID == workerID {
			out = append(out, rec)
		}
	}
	slices.SortFunc(out, func(a, b InterruptedWorkingRecord) int {
		return strings.Compare(a.ID, b.ID)
	})
	return out, nil
}

func salvageBackingDir(
	backingDir, quarantineDir string,
	knownPaths []layeredfs.QualifiedPath,
	op string,
	unknownScope bool,
) ([]layeredfs.QualifiedPath, string, bool, int64) {
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
	copiedBytes := copyTreeBestEffort(backingDir, filepath.Join(quarantineDir, "backing"))
	if len(out) == 0 && (copiedBytes > 0 || unknownScope) {
		unknownScope = true
	}
	return out, op, unknownScope, copiedBytes
}

func copyTreeBestEffort(srcDir, dstDir string) int64 {
	if strings.TrimSpace(srcDir) == "" {
		return 0
	}
	var total int64
	_ = filepath.WalkDir(srcDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, rErr := filepath.Rel(srcDir, p)
		if rErr != nil || rel == "." {
			return nil
		}
		target := filepath.Join(dstDir, rel)
		if d.IsDir() {
			_ = os.MkdirAll(target, 0o700)
			return nil
		}
		if d.Type().IsRegular() {
			_ = os.MkdirAll(filepath.Dir(target), 0o700)
			if sf, oErr := os.Open(p); oErr == nil {
				if df, cErr := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600); cErr == nil {
					n, _ := io.Copy(df, sf)
					total += n
					_ = df.Close()
				}
				_ = sf.Close()
			}
		}
		return nil
	})
	return total
}
