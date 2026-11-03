package recovery

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lycaon/lycaon/internal/fssync"
	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
	"github.com/lycaon/lycaon/internal/layeredfs/privategit"
)

// CoordinatorConfig configures a durable recovery and retention Coordinator.
type CoordinatorConfig struct {
	RootDir         string
	CheckpointStore *checkpoint.Store
	GitStore        *privategit.RetainedObjectStore
	GitManager      *privategit.Manager
	CurrentBootID   string
	ProcessCtrl     ProcessController
	MountReconciler MountReconciler
	FaultInjector   FaultInjector
	WriteHook       func(dir, name string, raw []byte) error
}

// Coordinator manages durable worker lifecycle journals, mount identities, inventory, and retention.
type Coordinator struct {
	cfg            CoordinatorConfig
	journalsDir    string
	mountsDir      string
	interruptedDir string
	retentionDir   string
	landingsDir    string
	inventoryPath  string
	mu             sync.Mutex
	seq            atomic.Uint64
}

// NewCoordinator creates or opens a durable recovery Coordinator at cfg.RootDir.
func NewCoordinator(cfg CoordinatorConfig) (*Coordinator, error) {
	if strings.TrimSpace(cfg.RootDir) == "" || cfg.CheckpointStore == nil {
		return nil, fmt.Errorf("recovery: RootDir and CheckpointStore are required")
	}
	if cfg.CurrentBootID == "" {
		cfg.CurrentBootID = "boot-default"
	}
	c := &Coordinator{
		cfg:            cfg,
		journalsDir:    filepath.Join(cfg.RootDir, "journals"),
		mountsDir:      filepath.Join(cfg.RootDir, "mounts"),
		interruptedDir: filepath.Join(cfg.RootDir, "interrupted"),
		retentionDir:   filepath.Join(cfg.RootDir, "retention"),
		landingsDir:    filepath.Join(cfg.RootDir, "landings"),
		inventoryPath:  filepath.Join(cfg.RootDir, "inventory.json"),
	}
	for _, d := range []string{c.cfg.RootDir, c.journalsDir, c.mountsDir, c.interruptedDir, c.retentionDir, c.landingsDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, fmt.Errorf("recovery: mkdir %s: %w", d, err)
		}
	}
	return c, nil
}

// RootDir returns the coordinator's persistent storage root.
func (c *Coordinator) RootDir() string { return c.cfg.RootDir }

func injectFault(c *Coordinator, phase TransitionPhase, step string) error {
	if c.cfg.FaultInjector != nil {
		return c.cfg.FaultInjector(phase, step)
	}
	return nil
}

// SaveWorkerJournal validates and atomically persists a WorkerLifecycleJournal.
func (c *Coordinator) SaveWorkerJournal(j WorkerLifecycleJournal) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return saveWorkerJournalLocked(c, j)
}

func saveWorkerJournalLocked(c *Coordinator, j WorkerLifecycleJournal) error {
	if strings.TrimSpace(j.WorkerID) == "" || strings.TrimSpace(j.ProjectID) == "" {
		return fmt.Errorf("%w: worker_id and project_id are required", ErrCorruptRecoveryMetadata)
	}
	if j.FormatVersion == 0 {
		j.FormatVersion = FormatVersion
	}
	if j.FormatVersion != FormatVersion {
		return fmt.Errorf("%w: got %d want %d", ErrUnsupportedFormatVersion, j.FormatVersion, FormatVersion)
	}
	if j.BootID == "" {
		j.BootID = c.cfg.CurrentBootID
	}
	j.UpdatedAt = time.Now().UTC()
	return writeAtomicJSON(c, c.journalsDir, j.WorkerID+".json", j)
}

// LoadWorkerJournal reads and strictly validates a persisted WorkerLifecycleJournal.
// Unknown or corrupt metadata is retained untouched on disk and refused with a typed error.
func (c *Coordinator) LoadWorkerJournal(workerID string) (WorkerLifecycleJournal, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return loadWorkerJournalLocked(c, workerID)
}

func loadWorkerJournalLocked(c *Coordinator, workerID string) (WorkerLifecycleJournal, error) {
	p := filepath.Join(c.journalsDir, workerID+".json")
	raw, err := os.ReadFile(p)
	if err != nil {
		return WorkerLifecycleJournal{}, err
	}
	var j WorkerLifecycleJournal
	if err := decodeVersionedJSON(raw, &j); err != nil {
		return WorkerLifecycleJournal{}, err
	}
	if j.WorkerID != workerID || j.ProjectID == "" {
		return WorkerLifecycleJournal{}, fmt.Errorf("%w: worker_id=%q expected=%q", ErrCorruptRecoveryMetadata, j.WorkerID, workerID)
	}
	return j, nil
}

// RecordTransition appends a durable phase transition to the worker's lifecycle journal.
func (c *Coordinator) RecordTransition(workerID, viewID string, phase TransitionPhase, state TransitionState, checkpointID, op string, paths []layeredfs.QualifiedPath, unknownScope bool) error {
	return recordTransition(c, workerID, viewID, phase, state, checkpointID, op, paths, unknownScope)
}

func recordTransition(
	c *Coordinator,
	workerID, viewID string,
	phase TransitionPhase,
	state TransitionState,
	checkpointID, op string,
	paths []layeredfs.QualifiedPath,
	unknownScope bool,
) error {
	if err := injectFault(c, phase, "before_journal_"+string(state)); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	j, err := loadWorkerJournalLocked(c, workerID)
	if err != nil {
		return err
	}
	seq := uint64(len(j.Transitions) + 1)
	rec := LifecycleTransitionRecord{
		Seq:          seq,
		Phase:        phase,
		State:        state,
		ViewID:       viewID,
		CheckpointID: checkpointID,
		Operation:    op,
		Paths:        append([]layeredfs.QualifiedPath(nil), paths...),
		UnknownScope: unknownScope,
		RecordedAt:   time.Now().UTC(),
	}
	j.Transitions = append(j.Transitions, rec)
	if state == StateBegin {
		j.ActivePhase = phase
		j.ActiveOperation = op
		j.ActivePaths = append([]layeredfs.QualifiedPath(nil), paths...)
		j.ActiveUnknownScope = unknownScope
	} else if j.ActivePhase == phase {
		j.ActivePhase = ""
		j.ActiveOperation = ""
		j.ActivePaths = nil
		j.ActiveUnknownScope = false
	}
	if phase == PhasePublication && state == StateCommitted && checkpointID != "" {
		j.LastPublishedCheckpointID = checkpointID
	}
	if err := saveWorkerJournalLocked(c, j); err != nil {
		return err
	}
	return injectFault(c, phase, "after_journal_"+string(state))
}

// RecordMountIdentity persists runtime mount and process lineage identity for a worker view.
func (c *Coordinator) RecordMountIdentity(rec MountIdentityRecord) error {
	return recordMountIdentity(c, rec)
}

func recordMountIdentity(c *Coordinator, rec MountIdentityRecord) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if rec.WorkerID == "" || rec.ViewID == "" {
		return fmt.Errorf("%w: worker_id and view_id required", ErrCorruptRecoveryMetadata)
	}
	rec.FormatVersion = FormatVersion
	if rec.BootID == "" {
		rec.BootID = c.cfg.CurrentBootID
	}
	rec.UpdatedAt = time.Now().UTC()
	return writeAtomicJSON(c, c.mountsDir, mountKey(rec.WorkerID, rec.ViewID)+".json", rec)
}

// LoadMountIdentity loads a worker view's recorded mount identity.
func (c *Coordinator) LoadMountIdentity(workerID, viewID string) (MountIdentityRecord, bool, error) {
	return loadMountIdentity(c, workerID, viewID)
}

func loadMountIdentity(c *Coordinator, workerID, viewID string) (MountIdentityRecord, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	p := filepath.Join(c.mountsDir, mountKey(workerID, viewID)+".json")
	raw, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return MountIdentityRecord{}, false, nil
		}
		return MountIdentityRecord{}, false, err
	}
	var rec MountIdentityRecord
	if err := decodeVersionedJSON(raw, &rec); err != nil {
		return MountIdentityRecord{}, false, err
	}
	if rec.WorkerID != workerID || rec.ViewID != viewID {
		return MountIdentityRecord{}, false, fmt.Errorf("%w: mount identity mismatch", ErrCorruptRecoveryMetadata)
	}
	return rec, true, nil
}

// RemoveMountIdentity deletes a settled worker view's mount identity record.
func (c *Coordinator) RemoveMountIdentity(workerID, viewID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	err := os.Remove(filepath.Join(c.mountsDir, mountKey(workerID, viewID)+".json"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func mountKey(workerID, viewID string) string {
	return workerID + "__" + viewID
}

func decodeVersionedJSON(raw []byte, dst any) error {
	var hdr struct {
		FormatVersion *int `json:"format_version"`
	}
	if err := json.Unmarshal(raw, &hdr); err != nil {
		return fmt.Errorf("%w: %w", ErrCorruptRecoveryMetadata, err)
	}
	if hdr.FormatVersion == nil {
		return fmt.Errorf("%w: missing format_version", ErrCorruptRecoveryMetadata)
	}
	if *hdr.FormatVersion != FormatVersion {
		return fmt.Errorf("%w: got %d want %d", ErrUnsupportedFormatVersion, *hdr.FormatVersion, FormatVersion)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("%w: %w", ErrCorruptRecoveryMetadata, err)
	}
	if dec.More() || dec.Decode(&struct{}{}) != io.EOF {
		return fmt.Errorf("%w: trailing data", ErrCorruptRecoveryMetadata)
	}
	return nil
}

func writeAtomicJSON(c *Coordinator, dir, name string, val any) error {
	raw, err := json.MarshalIndent(val, "", "  ")
	if err != nil {
		return err
	}
	if c.cfg.WriteHook != nil {
		if err := c.cfg.WriteHook(dir, name, raw); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "."+name+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := f.Name()
	_, wErr := f.Write(raw)
	cErr := f.Chmod(0o600)
	sErr := syncFile(f)
	closeErr := f.Close()
	if err := errors.Join(wErr, cErr, sErr, closeErr); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, filepath.Join(dir, name)); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return syncDir(dir)
}

func syncFile(f *os.File) error {
	if fssync.Relaxed() {
		return nil
	}
	return f.Sync()
}

func syncDir(dirPath string) error {
	if fssync.Relaxed() {
		return nil
	}
	df, err := os.Open(dirPath)
	if err != nil {
		return err
	}
	return errors.Join(df.Sync(), df.Close())
}
