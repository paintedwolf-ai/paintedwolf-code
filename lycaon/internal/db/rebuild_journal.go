package db

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/fssync"
)

// StoreUpgradeDirectoryPrefix and StoreUpgradeJournalSuffix identify local,
// non-exported publication recovery state. It is never user-clearable cache.
const StoreUpgradeDirectoryPrefix = ".store-upgrade-"
const StoreUpgradeJournalSuffix = ".upgrade.json"

// Publication exchanges two sealed databases. Before exchange, restart can
// rebuild from the original; after exchange, recovery retains the old file.
type rebuildJournal struct {
	Version             int    `json:"version"`
	Database            string `json:"database"`
	Directory           string `json:"directory"`
	DirectoryIdentity   string `json:"directory_identity"`
	State               string `json:"state"`
	OriginalIdentity    string `json:"original_identity,omitempty"`
	ReplacementIdentity string `json:"replacement_identity,omitempty"`
}

func rebuildMarker(path string) string { return path + StoreUpgradeJournalSuffix }

func writeRebuildJournal(path string, record rebuildJournal) error {
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	_, err = fseffect.Replace(fseffect.ReplaceRequest{Location: fseffect.PathLocation(rebuildMarker(path)), Source: bytes.NewReader(raw), Mode: 0o600})
	return err
}

func readRebuildJournal(path string) (rebuildJournal, error) {
	var record rebuildJournal
	file, err := os.Open(rebuildMarker(path))
	if err != nil {
		return record, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return record, err
	}
	if info.Size() > 16<<10 {
		return record, fmt.Errorf("store upgrade journal exceeds limit")
	}
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return record, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return record, fmt.Errorf("store upgrade journal has trailing content")
	}
	if record.Version != 2 || record.Database != filepath.Base(path) || record.Directory != filepath.Base(record.Directory) || !strings.HasPrefix(record.Directory, StoreUpgradeDirectoryPrefix) || record.DirectoryIdentity == "" {
		return record, fmt.Errorf("store upgrade journal has invalid identity")
	}
	if record.State != "building" && record.State != "prepared" && record.State != "installed" {
		return record, fmt.Errorf("store upgrade journal has invalid state")
	}
	if record.State != "building" && (record.OriginalIdentity == "" || record.ReplacementIdentity == "") {
		return record, fmt.Errorf("store upgrade journal is missing its database identity")
	}
	return record, nil
}

// ResumeStoreUpgrade runs under the installation lease, before restore, fresh
// initialization, or any SQLite connection. No elapsed-time limit changes its decision.
func ResumeStoreUpgrade(path string) (err error) {
	defer func() { err = ClassifyStoreFailure(err) }()
	record, err := readRebuildJournal(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if record.State == "building" {
		return discardRebuild(path, record)
	}
	if record.State == "installed" {
		return nil
	}
	identity, err := fspath.EntryIdentity(path)
	if err != nil {
		return err
	}
	if identity == record.OriginalIdentity {
		// A journal-unaware older build may have written since the interruption.
		// Its current database remains authoritative until the atomic exchange.
		return discardRebuild(path, record)
	}
	return finishRebuildPublication(path, record, nil)
}

// afterMove is an interruption seam: the move and its directory sync have
// already completed. Retrying reads filesystem identities, not an optimistic counter.
func finishRebuildPublication(path string, record rebuildJournal, afterMove func(string) error) error {
	root := filepath.Dir(path)
	work := filepath.Join(root, record.Directory)
	if err := requireRebuildIdentity(work, record.DirectoryIdentity); err != nil {
		return err
	}
	identity, err := fspath.EntryIdentity(path)
	if err != nil {
		return err
	}
	if identity == record.OriginalIdentity {
		if _, err := os.Lstat(filepath.Join(work, "original.db")); !os.IsNotExist(err) {
			if err != nil {
				return err
			}
			return fmt.Errorf("store upgrade archive destination occupied")
		}
		if err := requireSealedRebuild(path); err != nil {
			return err
		}
		if err := requireSealedRebuild(filepath.Join(work, "replacement.db")); err != nil {
			return err
		}
		if err := fseffect.ExchangeGuarded(root, record.Database, filepath.Join(record.Directory, "replacement.db"), record.OriginalIdentity, record.ReplacementIdentity); err != nil {
			return err
		}
	} else if identity != record.ReplacementIdentity {
		return fmt.Errorf("store upgrade destination identity changed")
	}
	if afterMove != nil {
		if err := afterMove("exchange"); err != nil {
			return err
		}
	}
	if err := resumeRebuildMove(root, filepath.Join(record.Directory, "replacement.db"), filepath.Join(record.Directory, "original.db"), record.OriginalIdentity); err != nil {
		return err
	}
	if afterMove != nil {
		if err := afterMove("archive"); err != nil {
			return err
		}
	}
	record.State = "installed"
	return writeRebuildJournal(path, record)
}

func requireSealedRebuild(path string) error {
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(path + suffix); !os.IsNotExist(err) {
			if err != nil {
				return err
			}
			return fmt.Errorf("store sidecar remains before exchange: %s", suffix)
		}
	}
	return nil
}

func resumeRebuildMove(root, from, to, identity string) error {
	destination := filepath.Join(root, to)
	if got, err := fspath.EntryIdentity(destination); err == nil {
		if got != identity {
			return fmt.Errorf("store upgrade destination identity changed: %s", to)
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	return fseffect.RenameGuarded(root, from, to, identity)
}

func requireRebuildIdentity(path, identity string) error {
	got, err := fspath.EntryIdentity(path)
	if err != nil {
		return err
	}
	if got != identity {
		return fmt.Errorf("store upgrade file identity changed")
	}
	return nil
}

// CompleteStoreUpgrade releases the raw pre-image only after a verified,
// self-contained recovery point and the live installation have reached readiness.
func CompleteStoreUpgrade(path string) error {
	record, err := readRebuildJournal(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if record.State != "installed" {
		return fmt.Errorf("cannot retire an unfinished store upgrade")
	}
	return discardRebuild(path, record)
}

func discardRebuild(path string, record rebuildJournal) error {
	work := filepath.Join(filepath.Dir(path), record.Directory)
	if err := requireRebuildIdentity(work, record.DirectoryIdentity); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.RemoveAll(work); err != nil {
		return err
	}
	if err := os.Remove(rebuildMarker(path)); err != nil && !os.IsNotExist(err) {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	return errors.Join(fssync.File(directory), directory.Close())
}
