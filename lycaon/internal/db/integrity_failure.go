package db

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fspath"
)

// IntegrityFailureSuffix is local health evidence bound to one physical store.
// It is excluded from backups; the health check discards it once the store it
// names has been replaced or removed.
const IntegrityFailureSuffix = ".integrity-failure.json"
const integrityFailureMaxBytes = 8192

type integrityFailureRecord struct {
	Version  int    `json:"version"`
	Identity string `json:"identity"`
	Detail   string `json:"detail"`
}

// CheckStoreHealth refuses recorded corruption before capture or store open,
// including when SQLite could not save the diagnosis in its own metadata.
// Evidence that names another physical file (a restored, fresh, or removed
// store) is stale and is discarded here, on every open path.
func CheckStoreHealth(path string) error {
	recordPath := path + IntegrityFailureSuffix
	file, err := os.Open(recordPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return storeIncompatible(RecoveryReasonIntegrityFailed, 0, fmt.Sprintf("read store quarantine: %v", err))
	}
	defer func() { _ = file.Close() }()
	raw, err := io.ReadAll(io.LimitReader(file, integrityFailureMaxBytes+1))
	if err != nil {
		return storeIncompatible(RecoveryReasonIntegrityFailed, 0, fmt.Sprintf("read store quarantine: %v", err))
	}
	var record integrityFailureRecord
	if len(raw) > integrityFailureMaxBytes || json.Unmarshal(raw, &record) != nil || record.Version != 1 || record.Identity == "" || record.Detail == "" {
		return storeIncompatible(RecoveryReasonIntegrityFailed, 0, "Store quarantine evidence is invalid or unsupported.")
	}
	identity, err := fspath.EntryIdentity(fspath.CanonicalPath(path))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return storeIncompatible(RecoveryReasonIntegrityFailed, 0, fmt.Sprintf("identify quarantined store: %v", err))
	}
	if err == nil && identity == record.Identity {
		return storeIncompatible(RecoveryReasonIntegrityFailed, 0, record.Detail)
	}
	if err := os.Remove(recordPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("discard stale store quarantine: %w", err)
	}
	return nil
}

func recordIntegrityFailure(health *storeHealth, failure *StoreIncompatibleError) error {
	// In-memory stores have no restart state.
	if health.path == "" {
		return nil
	}
	detail := failure.Detail
	if len(detail) > 1024 {
		detail = detail[:1024]
	}
	raw, err := json.Marshal(integrityFailureRecord{Version: 1, Identity: health.identity, Detail: detail})
	if err != nil {
		return err
	}
	_, err = fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.Location{Root: filepath.Dir(health.path), Rel: filepath.Base(health.path) + IntegrityFailureSuffix},
		Source:   bytes.NewReader(raw), Mode: 0o600,
	})
	return err
}

func bindStoreHealth(store *Store, path string) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve durable store: %w", err)
	}
	identity, err := fspath.EntryIdentity(fspath.CanonicalPath(path))
	if err != nil {
		return fmt.Errorf("identify durable store: %w", err)
	}
	store.health.path, store.health.identity = absolute, identity
	return nil
}
