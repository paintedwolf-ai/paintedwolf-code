package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fspath"
)

// A journal-unaware opener can initialize the temporarily absent pathname after
// a crash. Preserve that empty database rather than requiring manual repair.
// Never infer emptiness from its size alone or move a database with user state.
func preserveEmptyRebuildOccupant(path string, record rebuildJournal) error {
	identity, err := fspath.EntryIdentity(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if identity == record.ReplacementIdentity {
		return nil
	}
	var original string
	for _, file := range record.Original {
		if file.Suffix == "" {
			original = file.Identity
		}
	}
	if identity == original {
		return nil
	}
	root := filepath.Dir(path)
	if err := requireRebuildIdentity(filepath.Join(root, record.Directory, "original.db"), original); err != nil {
		return err
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(path + suffix); !os.IsNotExist(err) {
			if err != nil {
				return err
			}
			return fmt.Errorf("unexpected store upgrade occupant has a sidecar")
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("unexpected store upgrade occupant")
	}
	if info.Size() != 0 {
		reader, err := sql.Open("sqlite", fileDSN(path, "mode=ro"))
		if err != nil {
			return err
		}
		defer reader.Close()
		var tables, version, application int
		for _, check := range []struct {
			query string
			out   *int
		}{
			{`SELECT COUNT(*) FROM sqlite_schema`, &tables}, {`PRAGMA user_version`, &version}, {`PRAGMA application_id`, &application},
		} {
			if err := reader.QueryRowContext(context.Background(), check.query).Scan(check.out); err != nil {
				return err
			}
		}
		if tables != 0 || version != 0 || application != 0 {
			return fmt.Errorf("store upgrade destination contains new state")
		}
		if err := reader.Close(); err != nil {
			return err
		}
	}
	// A unique destination preserves the unexpected file without overwriting
	// evidence from an earlier retry. Only one atomic move is needed.
	occupant, err := os.MkdirTemp(filepath.Join(root, record.Directory), "empty-occupant-")
	if err != nil {
		return err
	}
	return fseffect.RenameGuarded(root, filepath.Base(path), filepath.Join(record.Directory, filepath.Base(occupant), "store.db"), identity)
}
