package checkpoint

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

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
	Kind       layeredfs.EntryKind `json:"kind"`
	Mode       os.FileMode         `json:"mode,omitempty"`
	SHA256     string              `json:"sha256,omitempty"`
	Size       int64               `json:"size,omitempty"`
	LinkTarget string              `json:"link_target,omitempty"`
	Xattrs     map[string][]byte   `json:"xattrs,omitempty"`
}

type hostRefUndo struct {
	Name   string `json:"name"`
	Before string `json:"before,omitempty"`
	After  string `json:"after"`
}

// PrepareHostTransaction persists undo before checkpoint effects join a host transaction.
// The host uses the built-in namespaced Git importer, whose refs can be restored by CAS.
func (pr *Promoter) PrepareHostTransaction(ctx context.Context, preview *PromotionPreview) error {
	if preview == nil || len(preview.Conflicts) != 0 {
		return ErrUnresolvedConflict
	}
	old, found, err := pr.loadHostTransaction()
	if err != nil {
		return err
	}
	if found && !old.RolledBack {
		return fmt.Errorf("checkpoint: unfinished host transaction requires recovery")
	}
	m, err := pr.cfg.Store.LoadManifest(preview.CheckpointID)
	if err != nil {
		return err
	}
	targets, err := pr.prepareHostTargets(preview)
	if err != nil {
		return err
	}
	refs, err := pr.prepareHostRefs(ctx, m)
	if err != nil {
		return err
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}
	roots, err := pr.hostRootIdentities()
	if err != nil {
		return err
	}
	return pr.saveHostTransaction(hostTransaction{Roots: roots, Version: 1, CheckpointID: m.CheckpointID, WorkerID: m.WorkerID, Targets: targets, Refs: refs})
}

// RollbackHostTransaction restores only recorded checkpoint effects, refusing divergence.
// The completion marker makes recovery idempotent even if the host's later undo crashes.
func (pr *Promoter) RollbackHostTransaction(ctx context.Context) error {
	tx, found, err := pr.loadHostTransaction()
	if err != nil || !found || tx.RolledBack {
		return err
	}
	if err := pr.validateHostTargets(tx.Targets); err != nil {
		return err
	}
	tx.RollingBack = true
	if err := pr.saveHostTransaction(tx); err != nil {
		return err
	}
	if err := pr.rollbackHostRefs(ctx, tx.Refs); err != nil {
		return err
	}
	if err := pr.restoreHostTargets(tx.Targets); err != nil {
		return err
	}
	// An applied component record must never suppress file/ref effects on retry.
	if err := pr.removeHostComponentRecord(tx); err != nil {
		return err
	}
	tx.RolledBack = true
	return pr.saveHostTransaction(tx)
}

func (pr *Promoter) removeHostComponentRecord(tx hostTransaction) error {
	// Validate identity through the manifest before constructing component filenames.
	m, err := pr.cfg.Store.LoadManifest(tx.CheckpointID)
	if err != nil {
		return err
	}
	if m.WorkerID != tx.WorkerID {
		return ErrOwnershipMismatch
	}
	for _, name := range []string{"tx-" + m.CheckpointID + ".json", "git-import-" + m.WorkerID + "--" + m.CheckpointID + ".json"} {
		if filepath.Base(name) != name {
			return ErrInvalidManifest
		}
		if err := os.Remove(filepath.Join(pr.cfg.Store.promotionsDir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return syncDir(pr.cfg.Store.promotionsDir)
}

func (pr *Promoter) loadHostTransaction() (hostTransaction, bool, error) {
	var tx hostTransaction
	raw, err := os.ReadFile(filepath.Join(pr.cfg.Store.promotionsDir, "host-transaction.json"))
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
	if err := pr.validateHostTransaction(tx); err != nil {
		return tx, true, err
	}
	return tx, true, nil
}

func (pr *Promoter) saveHostTransaction(tx hostTransaction) error {
	raw, err := json.Marshal(tx)
	if err != nil {
		return err
	}
	return writeAtomicJSONOrText(pr.cfg.Store.promotionsDir, "host-transaction.json", raw, 0600)
}

// recordHostPostimage records attributes from an owned staging object before publication.
// Filesystems may add attributes at creation; predicting an empty set loses those facts.
func (pr *Promoter) recordHostPostimage(path layeredfs.QualifiedPath, file *os.File) error {
	tx, found, err := pr.loadHostTransaction()
	if err != nil || !found || tx.RolledBack || tx.RollingBack {
		return err
	}
	attrs, err := readFileXattrs(file)
	if err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		return err
	}
	for index := range tx.Targets {
		target := &tx.Targets[index]
		if target.Path != path {
			continue
		}
		if target.After.Mode != info.Mode().Perm() {
			return ErrStalePreviewTargetChanged
		}
		target.After.Xattrs = attrs
		return pr.saveHostTransaction(tx)
	}
	return fmt.Errorf("checkpoint: unrecorded host target %s", pathKey(path))
}
