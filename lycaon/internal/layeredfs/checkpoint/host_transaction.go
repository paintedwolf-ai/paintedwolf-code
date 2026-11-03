package checkpoint

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/layeredfs"
)

// hostTransaction records checkpoint effects that precede the host's SQL commit.
// Its before-images describe the tree after the host's ordinary file writes;
// recovery restores those images before the host rolls its own journal back.
type hostTransaction struct {
	Version      int                         `json:"version"`
	CheckpointID string                      `json:"checkpoint_id"`
	WorkerID     string                      `json:"worker_id"`
	RollingBack  bool                        `json:"rolling_back,omitempty"`
	RolledBack   bool                        `json:"rolled_back,omitempty"`
	Roots        map[string]hostRootIdentity `json:"roots"`
	Targets      []hostTargetUndo            `json:"targets"`
	Refs         []hostRefUndo               `json:"refs,omitempty"`
}

type hostTargetUndo struct {
	Path   layeredfs.QualifiedPath `json:"path"`
	Before hostTargetImage         `json:"before"`
	After  hostTargetImage         `json:"after"`
}

type hostTargetImage struct {
	Kind           layeredfs.EntryKind      `json:"kind"`
	Mode           os.FileMode              `json:"mode,omitempty"`
	SHA256         string                   `json:"sha256,omitempty"`
	Size           int64                    `json:"size,omitempty"`
	LinkTarget     string                   `json:"link_target,omitempty"`
	Xattrs         map[string][]byte        `json:"xattrs,omitempty"`
	HardLinkSource *layeredfs.QualifiedPath `json:"hard_link_source,omitempty"`
	HardLinkGroup  string                   `json:"hard_link_group,omitempty"`
	RetainedSlot   string                   `json:"retained_slot,omitempty"`
	identity       string
	links          uint64
}

type hostRefUndo struct {
	Name   string `json:"name"`
	Before string `json:"before,omitempty"`
	After  string `json:"after"`
}

type hostPostimageRecord struct {
	Version      int                     `json:"version"`
	CheckpointID string                  `json:"checkpoint_id"`
	WorkerID     string                  `json:"worker_id"`
	Index        int                     `json:"index"`
	Path         layeredfs.QualifiedPath `json:"path"`
	Xattrs       map[string][]byte       `json:"xattrs,omitempty"`
}

func (host *HostPromotion) retainedDir() string {
	return filepath.Join(host.store.promotionsDir, "host-retained")
}

func (host *HostPromotion) postimagesDir() string {
	return filepath.Join(host.store.promotionsDir, "host-postimages")
}

// prepare persists undo before checkpoint effects join a host transaction.
// The host uses the built-in namespaced Git importer, whose refs can be restored by CAS.
func (host *HostPromotion) prepare(ctx context.Context, preview *PromotionPreview) error {
	if preview == nil || len(preview.Conflicts) != 0 {
		return ErrUnresolvedConflict
	}
	old, found, err := host.loadHostTransaction()
	if err != nil {
		return err
	}
	if found && !old.RolledBack {
		return fmt.Errorf("checkpoint: unfinished host transaction requires recovery")
	}
	_ = os.RemoveAll(host.retainedDir())
	_ = os.RemoveAll(host.postimagesDir())
	m, err := host.store.LoadManifest(preview.CheckpointID)
	if err != nil {
		return err
	}
	targets, err := host.prepareHostTargets(preview)
	if err != nil {
		return err
	}
	refs, err := host.prepareHostRefs(ctx, m)
	if err != nil {
		return err
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}
	roots, err := host.hostRootIdentities()
	if err != nil {
		return err
	}
	tx := hostTransaction{
		Roots:        roots,
		Version:      1,
		CheckpointID: m.CheckpointID,
		WorkerID:     m.WorkerID,
		Targets:      targets,
		Refs:         refs,
	}
	if err := host.saveHostTransaction(tx); err != nil {
		return err
	}
	idxMap := make(map[layeredfs.QualifiedPath]int, len(tx.Targets))
	for i, target := range tx.Targets {
		idxMap[target.Path] = i
	}
	host.activeTx = &tx
	host.activeIndex = idxMap
	return nil
}

// Rollback restores only recorded checkpoint effects, refusing divergence.
// The completion marker makes recovery idempotent even if the host's later undo crashes.
func (host *HostPromotion) Rollback(ctx context.Context) error {
	host.checkpoint.applyMu.Lock()
	defer host.checkpoint.applyMu.Unlock()
	tx, found, err := host.loadHostTransaction()
	if err != nil || !found || tx.RolledBack {
		return err
	}
	if err := host.validateHostTargets(tx); err != nil {
		return err
	}
	tx.RollingBack = true
	if err := host.saveHostTransaction(tx); err != nil {
		return err
	}
	if err := host.rollbackHostRefs(ctx, tx.Refs); err != nil {
		return err
	}
	if err := host.restoreHostTargets(tx.Targets); err != nil {
		return err
	}
	// An applied component record must never suppress file/ref effects on retry.
	if err := host.removeHostComponentRecord(tx); err != nil {
		return err
	}
	tx.RolledBack = true
	if err := host.saveHostTransaction(tx); err != nil {
		return err
	}
	_ = os.RemoveAll(host.retainedDir())
	return nil
}

func (host *HostPromotion) removeHostComponentRecord(tx hostTransaction) error {
	// Validate identity through the manifest before constructing component filenames.
	m, err := host.store.LoadManifest(tx.CheckpointID)
	if err != nil {
		return err
	}
	if m.WorkerID != tx.WorkerID {
		return ErrOwnershipMismatch
	}
	// Markers go first: without its record a marker is inert, but a stale marker
	// under a live record would suppress the effect on retry.
	if err := removeAppliedPathMarkers(host.store.promotionsDir, "tx-"+m.CheckpointID); err != nil {
		return err
	}
	for _, name := range []string{"tx-" + m.CheckpointID + ".json", "git-import-" + m.WorkerID + "--" + m.CheckpointID + ".json"} {
		if filepath.Base(name) != name {
			return ErrInvalidManifest
		}
		if err := os.Remove(filepath.Join(host.store.promotionsDir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return syncDir(host.store.promotionsDir)
}

func (host *HostPromotion) loadHostTransaction() (hostTransaction, bool, error) {
	var tx hostTransaction
	raw, err := os.ReadFile(filepath.Join(host.store.promotionsDir, "host-transaction.json"))
	if errors.Is(err, os.ErrNotExist) {
		return tx, false, nil
	}
	if err != nil {
		return tx, false, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&tx); err != nil {
		return tx, true, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return tx, true, ErrInvalidManifest
	}
	if err := host.foldHostPostimages(&tx); err != nil {
		return tx, true, err
	}
	if err := host.validateHostTransaction(tx); err != nil {
		return tx, true, err
	}
	return tx, true, nil
}

func (host *HostPromotion) foldHostPostimages(tx *hostTransaction) error {
	if tx.Version != 1 || !hostComponent(tx.CheckpointID) || !hostComponent(tx.WorkerID) {
		return ErrInvalidManifest
	}
	entries, err := os.ReadDir(host.postimagesDir())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			return ErrInvalidManifest
		}
		raw, err := os.ReadFile(filepath.Join(host.postimagesDir(), entry.Name()))
		if err != nil {
			return err
		}
		if err := applyHostPostimageSidecar(entry.Name(), raw, tx); err != nil {
			return err
		}
	}
	return nil
}

func applyHostPostimageSidecar(name string, raw []byte, tx *hostTransaction) error {
	var rec hostPostimageRecord
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&rec); err != nil {
		return ErrInvalidManifest
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return ErrInvalidManifest
	}
	if rec.Version != 1 || !hostComponent(rec.CheckpointID) || !hostComponent(rec.WorkerID) {
		return ErrInvalidManifest
	}
	if rec.WorkerID != tx.WorkerID {
		return ErrOwnershipMismatch
	}
	if rec.CheckpointID != tx.CheckpointID || rec.Index < 0 || rec.Index >= len(tx.Targets) {
		return ErrInvalidManifest
	}
	if name != fmt.Sprintf("target-%06d.json", rec.Index) || rec.Path != tx.Targets[rec.Index].Path {
		return ErrInvalidManifest
	}
	tx.Targets[rec.Index].After.Xattrs = cloneXattrs(rec.Xattrs)
	if !validHostImage(tx.Targets[rec.Index].After) {
		return ErrInvalidManifest
	}
	return nil
}

func (host *HostPromotion) saveHostTransaction(tx hostTransaction) error {
	raw, err := json.Marshal(tx)
	if err != nil {
		return err
	}
	if err := writeAtomicJSONOrText(host.store.promotionsDir, "host-transaction.json", raw, 0600); err != nil {
		return err
	}
	_ = os.RemoveAll(host.postimagesDir())
	return nil
}

// recordHostPostimage records attributes from an owned staging object before publication.
// Filesystems may add attributes at creation; predicting an empty set loses those facts.
func (host *HostPromotion) recordHostPostimage(path layeredfs.QualifiedPath, file *os.File) error {
	if host.activeTx == nil || host.activeIndex == nil {
		tx, found, err := host.loadHostTransaction()
		if err != nil || !found || tx.RolledBack || tx.RollingBack {
			return err
		}
		idxMap := make(map[layeredfs.QualifiedPath]int, len(tx.Targets))
		for i, target := range tx.Targets {
			idxMap[target.Path] = i
		}
		host.activeTx = &tx
		host.activeIndex = idxMap
	}
	if host.activeTx.RolledBack || host.activeTx.RollingBack {
		return nil
	}
	idx, ok := host.activeIndex[path]
	if !ok {
		return fmt.Errorf("checkpoint: unrecorded host target %s", pathKey(path))
	}
	attrs, err := readFileXattrs(file)
	if err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		return err
	}
	target := &host.activeTx.Targets[idx]
	if target.After.Mode != info.Mode().Perm() {
		return ErrStalePreviewTargetChanged
	}
	if maps.EqualFunc(target.After.Xattrs, attrs, bytes.Equal) {
		return nil
	}
	target.After.Xattrs = cloneXattrs(attrs)
	raw, err := json.Marshal(hostPostimageRecord{
		Version:      1,
		CheckpointID: host.activeTx.CheckpointID,
		WorkerID:     host.activeTx.WorkerID,
		Index:        idx,
		Path:         path,
		Xattrs:       target.After.Xattrs,
	})
	if err != nil {
		return err
	}
	return writeAtomicJSONOrText(host.store.promotionsDir, filepath.Join("host-postimages", fmt.Sprintf("target-%06d.json", idx)), raw, 0o600)
}
