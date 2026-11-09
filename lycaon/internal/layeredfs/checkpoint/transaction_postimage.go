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

type postimageRecord struct {
	Version       int                     `json:"version"`
	TransactionID string                  `json:"transaction_id"`
	Index         int                     `json:"index"`
	Path          layeredfs.QualifiedPath `json:"path"`
	Xattrs        map[string][]byte       `json:"xattrs,omitempty"`
}

func (txn *Transaction) foldPostimages(tx *transactionJournal) error {
	if tx.Version != transactionVersion || !safeComponent(tx.ID) {
		return ErrInvalidManifest
	}
	entries, err := os.ReadDir(txn.postimagesDir())
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
		raw, err := os.ReadFile(filepath.Join(txn.postimagesDir(), entry.Name()))
		if err != nil {
			return err
		}
		if err := applyPostimageSidecar(entry.Name(), raw, tx); err != nil {
			return err
		}
	}
	return nil
}

func applyPostimageSidecar(name string, raw []byte, tx *transactionJournal) error {
	var rec postimageRecord
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&rec); err != nil {
		return ErrInvalidManifest
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return ErrInvalidManifest
	}
	if rec.Version != transactionVersion || rec.TransactionID != tx.ID || rec.Index < 0 || rec.Index >= len(tx.Targets) {
		return ErrInvalidManifest
	}
	if name != fmt.Sprintf("target-%06d.json", rec.Index) || rec.Path != tx.Targets[rec.Index].Path {
		return ErrInvalidManifest
	}
	tx.Targets[rec.Index].After.Xattrs = cloneXattrs(rec.Xattrs)
	if !validImage(tx.Targets[rec.Index].After) {
		return ErrInvalidManifest
	}
	return nil
}

// recordPostimage records attributes from an owned staging object before publication.
// Filesystems may add attributes at creation; predicting an empty set loses those facts.
func (txn *Transaction) recordPostimage(path layeredfs.QualifiedPath, file *os.File) error {
	if txn.journal.RollingBack {
		return nil
	}
	idx, ok := txn.index[path]
	if !ok {
		return fmt.Errorf("checkpoint: unrecorded promotion target %s", pathKey(path))
	}
	attrs, err := readFileXattrs(file)
	if err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		return err
	}
	target := &txn.journal.Targets[idx]
	if target.After.Mode != info.Mode().Perm() {
		return ErrStalePreviewTargetChanged
	}
	if maps.EqualFunc(target.After.Xattrs, attrs, bytes.Equal) {
		return nil
	}
	target.After.Xattrs = cloneXattrs(attrs)
	raw, err := json.Marshal(postimageRecord{
		Version:       transactionVersion,
		TransactionID: txn.journal.ID,
		Index:         idx,
		Path:          path,
		Xattrs:        target.After.Xattrs,
	})
	if err != nil {
		return err
	}
	return writeAtomicJSONOrText(txn.dir, filepath.Join("postimages", fmt.Sprintf("target-%06d.json", idx)), raw, 0o600)
}
