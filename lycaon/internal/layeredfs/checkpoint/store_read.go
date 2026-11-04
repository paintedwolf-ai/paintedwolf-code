package checkpoint

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/layeredfs"
)

// LoadManifest reads, parses, and validates a published checkpoint manifest and its blobs.
func (s *Store) LoadManifest(checkpointID string) (Manifest, error) {
	if strings.TrimSpace(checkpointID) == "" {
		return Manifest{}, ErrCheckpointNotFound
	}
	if !isSafeStoreID(checkpointID) {
		return Manifest{}, fmt.Errorf("%w: invalid checkpoint id %q", ErrInvalidManifest, checkpointID)
	}
	manifestPath := filepath.Join(s.checkpointsDir, checkpointID, "manifest.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Manifest{}, ErrCheckpointNotFound
		}
		return Manifest{}, err
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return Manifest{}, fmt.Errorf("%w: %w", ErrInvalidManifest, err)
	}
	if m.FormatVersion != FormatVersion || m.CheckpointID != checkpointID {
		return Manifest{}, fmt.Errorf("%w: version=%d id=%q", ErrInvalidManifest, m.FormatVersion, m.CheckpointID)
	}
	if err := s.validateManifestBlobs(m); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

func (s *Store) validateManifestBlobs(m Manifest) error {
	for _, entry := range m.Entries {
		if !entry.Deleted && entry.Kind == layeredfs.EntryRegular {
			if _, err := s.VerifyBlob(entry.Final.SHA256, entry.Final.Size); err != nil {
				return err
			}
		}
		if entry.Base.Kind == layeredfs.EntryRegular && entry.Base.SHA256 != "" {
			if _, err := s.VerifyBlob(entry.Base.SHA256, entry.Base.Size); err != nil {
				return err
			}
		}
	}
	return nil
}

// LatestCheckpointID returns the ID of the most recently published checkpoint.
func (s *Store) LatestCheckpointID() (string, error) {
	raw, err := os.ReadFile(filepath.Join(s.rootDir, "latest"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", ErrCheckpointNotFound
		}
		return "", err
	}
	id := strings.TrimSpace(string(raw))
	if id == "" {
		return "", ErrCheckpointNotFound
	}
	return id, nil
}

// LoadLatestManifest loads and validates the most recently published checkpoint manifest.
func (s *Store) LoadLatestManifest() (Manifest, error) {
	id, err := s.LatestCheckpointID()
	if err != nil {
		return Manifest{}, err
	}
	return s.LoadManifest(id)
}

// ReadEntryContent reads the immutable published content bytes for a regular file in checkpointID.
func (s *Store) ReadEntryContent(checkpointID string, qpath layeredfs.QualifiedPath) ([]byte, error) {
	m, err := s.LoadManifest(checkpointID)
	if err != nil {
		return nil, err
	}
	for _, e := range m.Entries {
		if e.Path == qpath {
			if e.Deleted || e.Kind != layeredfs.EntryRegular {
				return nil, fs.ErrNotExist
			}
			return s.ReadBlob(e.Final.SHA256)
		}
	}
	return nil, fs.ErrNotExist
}

// ReadBaseContent reads the immutable captured base bytes for a regular file in checkpointID.
func (s *Store) ReadBaseContent(checkpointID string, qpath layeredfs.QualifiedPath) ([]byte, error) {
	m, err := s.LoadManifest(checkpointID)
	if err != nil {
		return nil, err
	}
	for _, e := range m.Entries {
		if e.Path == qpath {
			if e.Base.Kind != layeredfs.EntryRegular {
				return nil, fs.ErrNotExist
			}
			return s.ReadBlob(e.Base.SHA256)
		}
	}
	return nil, fs.ErrNotExist
}

// OpenParentLayer mounts a published checkpoint as a read-only ParentLayer.
func (s *Store) OpenParentLayer(checkpointID string) (*CheckpointParentLayer, error) {
	m, err := s.LoadManifest(checkpointID)
	if err != nil {
		return nil, err
	}
	return NewCheckpointParentLayer(s, m)
}

// LoadRetentionRecord loads the retention record for checkpointID.
func (s *Store) LoadRetentionRecord(checkpointID string) (RetentionRecord, error) {
	if !isSafeStoreID(checkpointID) {
		return RetentionRecord{}, fmt.Errorf("%w: invalid checkpoint id %q", ErrInvalidManifest, checkpointID)
	}
	raw, err := os.ReadFile(filepath.Join(s.retentionDir, checkpointID+".json"))
	if err != nil {
		return RetentionRecord{}, err
	}
	var rec RetentionRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return RetentionRecord{}, err
	}
	return rec, nil
}

// LoadPromotionRecord loads a durable promotion journal record by transaction ID.
func (s *Store) LoadPromotionRecord(txID string) (PromotionJournalRecord, bool, error) {
	if !isSafeStoreID(txID) {
		return PromotionJournalRecord{}, false, fmt.Errorf("%w: invalid transaction id %q", ErrInvalidManifest, txID)
	}
	raw, err := os.ReadFile(filepath.Join(s.promotionsDir, txID+".json"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return PromotionJournalRecord{}, false, nil
		}
		return PromotionJournalRecord{}, false, err
	}
	var rec PromotionJournalRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return PromotionJournalRecord{}, false, err
	}
	applied, err := loadAppliedPathMarkers(s.promotionsDir, txID)
	if err != nil {
		return PromotionJournalRecord{}, false, err
	}
	rec.AppliedPaths = append(rec.AppliedPaths, applied...)
	return rec, true, nil
}

// appliedPathMarker is the per-path progress fact behind a promotion record. One
// small durable file per path keeps a promotion's journal writes linear.
type appliedPathMarker struct {
	Index int                     `json:"index"`
	Path  layeredfs.QualifiedPath `json:"path"`
}

func appliedPathMarkersDir(promotionsDir, txID string) string {
	return filepath.Join(promotionsDir, txID+".applied")
}

func recordAppliedPath(promotionsDir, txID string, index int, path layeredfs.QualifiedPath) error {
	raw, err := json.Marshal(appliedPathMarker{Index: index, Path: path})
	if err != nil {
		return err
	}
	return writeAtomicJSONOrText(promotionsDir, filepath.Join(txID+".applied", fmt.Sprintf("path-%09d.json", index)), raw, 0o600)
}

func loadAppliedPathMarkers(promotionsDir, txID string) ([]layeredfs.QualifiedPath, error) {
	dir := appliedPathMarkersDir(promotionsDir, txID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	paths := make([]layeredfs.QualifiedPath, 0, len(entries))
	for _, entry := range entries {
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		var marker appliedPathMarker
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&marker); err != nil {
			return nil, fmt.Errorf("%w: applied path marker %s: %w", ErrInvalidManifest, entry.Name(), err)
		}
		if entry.Name() != fmt.Sprintf("path-%09d.json", marker.Index) || !filepath.IsLocal(marker.Path.Rel) {
			return nil, fmt.Errorf("%w: applied path marker %s", ErrInvalidManifest, entry.Name())
		}
		paths = append(paths, marker.Path)
	}
	return paths, nil
}

func removeAppliedPathMarkers(promotionsDir, txID string) error {
	if err := os.RemoveAll(appliedPathMarkersDir(promotionsDir, txID)); err != nil {
		return err
	}
	return syncDir(promotionsDir)
}

func (pr *Promoter) loadPromotionRecord(txID string) (PromotionJournalRecord, bool, error) {
	return pr.cfg.Store.LoadPromotionRecord(txID)
}

func (pr *Promoter) savePromotionRecord(rec PromotionJournalRecord) error {
	if !isSafeStoreID(rec.TransactionID) {
		return fmt.Errorf("%w: invalid transaction id %q", ErrInvalidManifest, rec.TransactionID)
	}
	raw, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomicJSONOrText(pr.cfg.Store.promotionsDir, rec.TransactionID+".json", raw, 0o600)
}
