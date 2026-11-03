package checkpoint

import (
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
		return Manifest{}, fmt.Errorf("%w: %v", ErrInvalidManifest, err)
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
	return rec, true, nil
}

func (pr *Promoter) loadPromotionRecord(txID string) (PromotionJournalRecord, bool, error) {
	return pr.cfg.Store.LoadPromotionRecord(txID)
}

func (pr *Promoter) savePromotionRecord(rec PromotionJournalRecord) error {
	raw, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomicJSONOrText(pr.cfg.Store.promotionsDir, rec.TransactionID+".json", raw, 0o600)
}
