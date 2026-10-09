package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"modernc.org/sqlite"

	"github.com/lycaon/lycaon/internal/fssync"
)

var (
	// ErrStoreIncompatible marks a store that requires recovery.
	ErrStoreIncompatible = errors.New("store incompatible with this build")
)

// SchemaVersion identifies the baseline schema.
const SchemaVersion = 1

const cleanShutdownMetaKey = "clean_shutdown"

// hostOwnerRole is the people.role a new store seeds.
const hostOwnerRole = "owner"

// ReadUserVersion returns PRAGMA user_version on an open store.
func ReadUserVersion(ctx context.Context, sqlDB DBTX) (int, error) {
	var v int
	if err := sqlDB.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&v); err != nil {
		return 0, err
	}
	return v, nil
}

func hasProjectsTable(ctx context.Context, sqlDB *sql.DB) (bool, error) {
	var n int
	err := sqlDB.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='projects'`,
	).Scan(&n)
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// CreateSnapshot publishes a verified online SQLite snapshot at destPath.
func CreateSnapshot(ctx context.Context, sqlDB DBTX, destPath string) error {
	if sqlDB == nil {
		return fmt.Errorf("create snapshot: database required")
	}
	version, err := ReadUserVersion(ctx, sqlDB)
	if err != nil {
		return fmt.Errorf("create snapshot: read source version: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(destPath), "."+filepath.Base(destPath)+".snapshot-*")
	if err != nil {
		return fmt.Errorf("create snapshot: create staging path: %w", err)
	}
	tmpPath := tmp.Name()
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("create snapshot: close staging path: %w", err)
	}
	if err := os.Remove(tmpPath); err != nil {
		return fmt.Errorf("create snapshot: prepare staging path: %w", err)
	}
	defer func() {
		_ = os.Remove(tmpPath)
		_ = RemoveStoreSidecars(tmpPath)
	}()

	connectionDB, err := snapshotConnectionDB(sqlDB)
	if err != nil {
		return fmt.Errorf("create snapshot: %w", err)
	}
	conn, err := connectionDB.Conn(ctx)
	if err != nil {
		return fmt.Errorf("create snapshot: source connection: %w", err)
	}
	defer func() { _ = conn.Close() }()
	err = conn.Raw(func(driverConn any) error {
		backuper, ok := driverConn.(interface {
			NewBackup(string) (*sqlite.Backup, error)
		})
		if !ok {
			return fmt.Errorf("SQLite driver does not support online backup")
		}
		backup, backupErr := backuper.NewBackup(tmpPath)
		if backupErr != nil {
			return backupErr
		}
		finished := false
		defer func() {
			if !finished {
				_ = backup.Finish()
			}
		}()
		for {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			more, stepErr := backup.Step(128)
			if stepErr != nil {
				return stepErr
			}
			if !more {
				break
			}
		}
		if finishErr := backup.Finish(); finishErr != nil {
			return finishErr
		}
		finished = true
		return nil
	})
	if err != nil {
		return fmt.Errorf("create snapshot: online backup: %w", err)
	}
	if err := verifySnapshot(ctx, tmpPath, version); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, defaultDBFileMode); err != nil {
		return fmt.Errorf("create snapshot: chmod: %w", err)
	}
	// #nosec G304 -- os.CreateTemp produced tmpPath.
	file, err := os.Open(tmpPath)
	if err != nil {
		return fmt.Errorf("create snapshot: open for sync: %w", err)
	}
	if err := fssync.File(file); err != nil {
		_ = file.Close()
		return fmt.Errorf("create snapshot: sync: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("create snapshot: close: %w", err)
	}
	if err := os.Rename(tmpPath, destPath); err != nil {
		return fmt.Errorf("create snapshot: install: %w", err)
	}
	dir, err := os.Open(filepath.Dir(destPath))
	if err != nil {
		return fmt.Errorf("create snapshot: open destination directory: %w", err)
	}
	if err := fssync.File(dir); err != nil {
		_ = dir.Close()
		return fmt.Errorf("create snapshot: sync destination directory: %w", err)
	}
	return dir.Close()
}

type writerDatabase interface {
	writerDB() *sql.DB
}

func snapshotConnectionDB(database DBTX) (*sql.DB, error) {
	switch typed := database.(type) {
	case *sql.DB:
		return typed, nil
	case writerDatabase:
		if writer := typed.writerDB(); writer != nil {
			return writer, nil
		}
	}
	return nil, errors.New("database does not expose an online-backup connection")
}

func verifySnapshot(ctx context.Context, path string, expectedVersion int) error {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return fmt.Errorf("create snapshot: open verification database: %w", err)
	}
	defer func() { _ = db.Close() }()
	var integrity string
	if err := db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&integrity); err != nil {
		return fmt.Errorf("create snapshot: quick_check: %w", err)
	}
	if integrity != "ok" {
		return fmt.Errorf("create snapshot: quick_check: %s", integrity)
	}
	version, err := ReadUserVersion(ctx, db)
	if err != nil {
		return fmt.Errorf("create snapshot: read snapshot version: %w", err)
	}
	if version != expectedVersion {
		return fmt.Errorf("create snapshot: user_version %d, want %d", version, expectedVersion)
	}
	return nil
}

// execFreshSchema applies the baseline schema atomically.
func execFreshSchema(ctx context.Context, sqlDB *sql.DB) error {
	schema, err := schemaFS.ReadFile("schema.sql")
	if err != nil {
		return fmt.Errorf("read schema: %w", err)
	}
	// auto_vacuum is fixed when the first table is created.
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire fresh-schema connection: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, `PRAGMA auto_vacuum = INCREMENTAL`); err != nil {
		return fmt.Errorf("enable incremental auto_vacuum: %w", err)
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin fresh schema: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, string(schema)); err != nil {
		return fmt.Errorf("exec schema: %w", err)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, SchemaVersion)); err != nil {
		return fmt.Errorf("stamp user_version: %w", err)
	}
	if err := New(tx).InsertPerson(ctx, InsertPersonParams{
		ID:        uuid.NewString(),
		Role:      hostOwnerRole,
		CreatedAt: FormatTime(time.Now()),
	}); err != nil {
		return fmt.Errorf("seed host owner: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit fresh schema: %w", err)
	}
	return nil
}

// prepareStoreSchema creates or validates the baseline store.
func prepareStoreSchema(ctx context.Context, sqlDB *sql.DB, created bool) error {
	ver, err := ReadUserVersion(ctx, sqlDB)
	if err != nil {
		return fmt.Errorf("read user_version: %w", err)
	}
	hasProjects, err := hasProjectsTable(ctx, sqlDB)
	if err != nil {
		return fmt.Errorf("detect projects table: %w", err)
	}

	if created && !hasProjects && ver == 0 {
		return execFreshSchema(ctx, sqlDB)
	}
	if !hasProjects && ver == 0 {
		return storeIncompatible(RecoveryReasonIntegrityFailed, ver, "store file has no baseline schema")
	}

	if err := CheckBaseline(ctx, sqlDB); err != nil {
		return err
	}
	return prepareIntegrity(ctx, sqlDB)
}

// referenceShape reads schema.sql into an in-memory store.
func referenceShape(ctx context.Context) (Shape, error) {
	ref, err := sql.Open("sqlite", "file:schemaref?mode=memory&cache=private")
	if err != nil {
		return Shape{}, fmt.Errorf("open schema reference: %w", err)
	}
	defer func() { _ = ref.Close() }()
	if err := ref.PingContext(ctx); err != nil {
		return Shape{}, fmt.Errorf("ping schema reference: %w", err)
	}
	schema, err := schemaFS.ReadFile("schema.sql")
	if err != nil {
		return Shape{}, fmt.Errorf("read schema: %w", err)
	}
	if _, err := ref.ExecContext(ctx, string(schema)); err != nil {
		return Shape{}, fmt.Errorf("exec schema reference: %w", err)
	}
	return ReadShape(ctx, ref)
}

// CheckBaseline requires the current revision and physical schema after any upgrade.
func CheckBaseline(ctx context.Context, sqlDB DBTX) error {
	ver, err := ReadUserVersion(ctx, sqlDB)
	if err != nil {
		return fmt.Errorf("read user_version: %w", err)
	}
	if ver != SchemaVersion {
		return storeIncompatible(RecoveryReasonSchemaMismatch, ver,
			fmt.Sprintf("schema_version %d does not match %d", ver, SchemaVersion))
	}
	diff, err := BaselineShapeDiff(ctx, sqlDB)
	if err != nil {
		return err
	}
	if len(diff) == 0 {
		return nil
	}
	return storeIncompatible(RecoveryReasonSchemaMismatch, ver, summarizeShapeDiff(diff))
}

// BaselineShapeDiff lists how an open store's structure differs from
// schema.sql. An empty result means the store matches the baseline.
func BaselineShapeDiff(ctx context.Context, sqlDB DBTX) ([]string, error) {
	want, err := referenceShape(ctx)
	if err != nil {
		return nil, err
	}
	got, err := ReadShape(ctx, sqlDB)
	if err != nil {
		return nil, fmt.Errorf("read store shape: %w", err)
	}
	return want.Diff(got), nil
}

// ShapeDigest fingerprints an open store's structure.
func ShapeDigest(ctx context.Context, sqlDB DBTX) (string, error) {
	shape, err := ReadShape(ctx, sqlDB)
	if err != nil {
		return "", fmt.Errorf("read store shape: %w", err)
	}
	return shapeDigest(shape), nil
}

// BaselineShapeDigest fingerprints the structure schema.sql declares.
func BaselineShapeDigest(ctx context.Context) (string, error) {
	shape, err := referenceShape(ctx)
	if err != nil {
		return "", err
	}
	return shapeDigest(shape), nil
}

// shapeDigest serializes a shape in a stable order before hashing so two runs
// over the same structure agree.
func shapeDigest(shape Shape) string {
	sum := sha256.New()
	tables := make([]string, 0, len(shape.Columns))
	for table := range shape.Columns {
		tables = append(tables, table)
	}
	sort.Strings(tables)
	for _, table := range tables {
		fmt.Fprintf(sum, "table\x00%s\n", table)
		for _, col := range shape.Columns[table] {
			fmt.Fprintf(sum, "column\x00%s\x00%s\x00%d\x00%s\x00%d\n",
				col.Name, col.Type, col.NotNull, nullString(col.Default), col.PKPosition)
		}
	}
	for _, group := range [][]string{shape.Tables, shape.Indexes, shape.Triggers, shape.Views} {
		for _, text := range group {
			fmt.Fprintf(sum, "sql\x00%s\n", text)
		}
		fmt.Fprint(sum, "group\x00\n")
	}
	return hex.EncodeToString(sum.Sum(nil))
}

func summarizeShapeDiff(diff []string) string {
	n := 3
	if len(diff) < n {
		n = len(diff)
	}
	return strings.Join(diff[:n], "; ")
}

func lastShutdownWasClean(ctx context.Context, sqlDB DBTX) (bool, error) {
	var value string
	err := sqlDB.QueryRowContext(ctx, `SELECT value FROM store_meta WHERE key = ?`, cleanShutdownMetaKey).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return value == "1", err
}

func markShutdownState(ctx context.Context, sqlDB DBTX, clean bool) error {
	value := "0"
	if clean {
		value = "1"
	}
	_, err := sqlDB.ExecContext(ctx, `
INSERT INTO store_meta(key, value) VALUES(?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value`, cleanShutdownMetaKey, value)
	return err
}

// storeSidecarSuffixes names database-specific journal companions.
var storeSidecarSuffixes = []string{"-wal", "-shm", "-journal"}

// StoreSidecarSuffixes returns the SQLite sidecar suffixes for a database path.
func StoreSidecarSuffixes() []string {
	return append([]string(nil), storeSidecarSuffixes...)
}

// RemoveStoreSidecars deletes journal companions before file replacement.
func RemoveStoreSidecars(dbPath string) error {
	if strings.TrimSpace(dbPath) == "" {
		return nil
	}
	for _, suffix := range storeSidecarSuffixes {
		if err := os.Remove(dbPath + suffix); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove sidecar %s: %w", dbPath+suffix, err)
		}
	}
	return nil
}
