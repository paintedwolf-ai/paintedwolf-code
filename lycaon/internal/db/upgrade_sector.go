package db

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// probeUpgradeSector reads the actual SQLite VFS journal sector declaration.
// The disposable database uses the migration connection's own VFS and volume.
func probeUpgradeSector(ctx context.Context, root string) (uint64, error) {
	dir, err := os.MkdirTemp(root, ".upgrade-sector-")
	if err != nil {
		return 0, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	path := filepath.Join(dir, "probe.db")
	database, err := sql.Open("sqlite", fileDSN(path, writerDSNQuery+"&_pragma=journal_mode(delete)"))
	if err != nil {
		return 0, err
	}
	defer func() { _ = database.Close() }()
	database.SetMaxOpenConns(1)
	if _, err := database.ExecContext(ctx, "CREATE TABLE probe(value TEXT); INSERT INTO probe VALUES('before')"); err != nil {
		return 0, err
	}
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, "UPDATE probe SET value='after'"); err != nil {
		return 0, err
	}
	journal, err := os.Open(path + "-journal")
	if err != nil {
		return 0, err
	}
	var word [4]byte
	_, readErr := journal.ReadAt(word[:], 20)
	if err := errors.Join(readErr, journal.Close()); err != nil {
		return 0, err
	}
	sector := uint64(binary.BigEndian.Uint32(word[:]))
	if sector < 512 || sector > 65536 || sector&(sector-1) != 0 {
		return 0, fmt.Errorf("invalid SQLite journal sector size %d", sector)
	}
	return sector, nil
}
