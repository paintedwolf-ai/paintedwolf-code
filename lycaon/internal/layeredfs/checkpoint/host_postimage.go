package checkpoint

import (
	"bytes"
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

type hostPostimageRecord struct {
	Version      int                     `json:"version"`
	CheckpointID string                  `json:"checkpoint_id"`
	WorkerID     string                  `json:"worker_id"`
	Index        int                     `json:"index"`
	Path         layeredfs.QualifiedPath `json:"path"`
	Xattrs       map[string][]byte       `json:"xattrs,omitempty"`
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
