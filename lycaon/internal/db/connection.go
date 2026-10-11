// Package db opens the SQLite store and applies its schema.
package db

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/configdir"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

//go:embed schema.sql
var schemaFS embed.FS

const (
	defaultDBFileMode = 0o600
	writerOpenConns   = 1
	readerOpenConns   = 8
	// Commit acknowledgement covers the WAL sync, including editor receipts.
	writerDSNQuery = "_txlock=immediate&_pragma=foreign_keys(on)&_pragma=busy_timeout(10000)&_pragma=synchronous(full)"
	readerDSNQuery = "mode=ro&_pragma=query_only(on)&_pragma=foreign_keys(on)&_pragma=ignore_check_constraints(off)&_pragma=busy_timeout(10000)"
)

// runningAppVersion is stamped into store_meta on Open.
var runningAppVersion = "dev"

// SetRunningAppVersion sets the version stamped by Open.
func SetRunningAppVersion(v string) {
	if v == "" {
		v = "dev"
	}
	runningAppVersion = v
}

// DefaultPath returns the default SQLite path under UserConfigDir (store.db).
func DefaultPath() (string, error) {
	dir, err := configdir.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "store.db"), nil
}

// StoreOptions configures non-default store connection behavior.
type StoreOptions struct {
	// FastTestSync trades writer durability on power loss for test speed.
	FastTestSync bool
}

// Open prepares the schema and returns a pooled handle.
func Open(dbPath string) (*Store, error) {
	return OpenWithOptions(context.Background(), dbPath, UpgradeHooks{}, StoreOptions{})
}

// OpenWithOptions opens a store after recognized upgrades and verified installation recovery capture.
// The caller holds the installation lease throughout recovery, upgrading, and serving.
func OpenWithOptions(ctx context.Context, dbPath string, hooks UpgradeHooks, opts StoreOptions) (*Store, error) {
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create db directory: %w", err)
	}

	created := false
	if info, err := os.Stat(dbPath); err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("inspect database: %w", err)
		}
		created = true
	} else if info.Size() == 0 {
		if emptyStoreHasRecoveryArtifacts(dbPath) {
			return nil, storeIncompatible(RecoveryReasonIntegrityFailed, 0, "store file is empty")
		}
		created = true
	}

	if !created {
		if err := upgradeExisting(ctx, dbPath, hooks, false); err != nil {
			return nil, classifyStoreOpenFailure(err)
		}
	}

	writer, err := openWriter(ctx, dbPath, opts)
	if err != nil {
		return nil, classifyStoreOpenFailure(err)
	}

	if created {
		if err := prepareStoreSchema(ctx, writer, true); err != nil {
			_ = writer.Close()
			return nil, classifyStoreOpenFailure(err)
		}
	}

	auditDue := false
	if !created {
		clean, err := lastShutdownWasClean(ctx, writer)
		if err != nil {
			_ = writer.Close()
			return nil, fmt.Errorf("read shutdown state: %w", err)
		}
		auditDue = !clean
		if err := prepareAuditState(ctx, writer, auditDue); err != nil {
			_ = writer.Close()
			return nil, storeIncompatible(RecoveryReasonIntegrityFailed, SchemaVersion, err.Error())
		}
	}
	if err := markShutdownState(ctx, writer, false); err != nil {
		_ = writer.Close()
		return nil, fmt.Errorf("mark store open: %w", err)
	}
	if err := recordAppVersion(ctx, writer, created); err != nil {
		_ = writer.Close()
		return nil, classifyStoreOpenFailure(err)
	}

	if created {
		if err := os.Chmod(dbPath, defaultDBFileMode); err != nil {
			_ = writer.Close()
			return nil, fmt.Errorf("chmod database: %w", err)
		}
	} else {
		_ = os.Chmod(dbPath, defaultDBFileMode)
	}

	reader, err := openReader(ctx, dbPath)
	if err != nil {
		_ = writer.Close()
		return nil, classifyStoreOpenFailure(err)
	}
	store := newStore(writer, reader)
	if filepath.IsAbs(dbPath) {
		store.dataDir = filepath.Dir(dbPath)
	}
	store.integrityAuditDue = auditDue
	return store, nil
}

// UpgradeRecoveryDirName is the directory beside a store that holds the
// pre-upgrade recovery snapshots of that store.
const UpgradeRecoveryDirName = "upgrade-recovery"

// Recovery artifacts make a zero-byte store ambiguous.
func emptyStoreHasRecoveryArtifacts(dbPath string) bool {
	for _, artifact := range []string{dbPath + "-wal", dbPath + "-shm", filepath.Join(filepath.Dir(dbPath), UpgradeRecoveryDirName)} {
		if _, err := os.Stat(artifact); err == nil || !os.IsNotExist(err) {
			return true
		}
	}
	return false
}

// OpenReadOnly opens an existing store without schema preparation or write access.
func OpenReadOnly(ctx context.Context, dbPath string) (*sql.DB, error) {
	return openReader(ctx, dbPath)
}

func openReader(ctx context.Context, dbPath string) (*sql.DB, error) {
	dsn := fileDSN(dbPath, readerDSNQuery)
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open read-only database: %w", err)
	}
	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("ping read-only database: %w", err)
	}
	sqlDB.SetMaxOpenConns(readerOpenConns)
	sqlDB.SetMaxIdleConns(readerOpenConns)
	return sqlDB, nil
}

// unservableStoreCodes identifies storage faults that require recovery.
var unservableStoreCodes = map[int]struct{}{
	sqlite3.SQLITE_CORRUPT:  {},
	sqlite3.SQLITE_NOTADB:   {},
	sqlite3.SQLITE_CANTOPEN: {},
	sqlite3.SQLITE_IOERR:    {},
	sqlite3.SQLITE_PERM:     {},
	sqlite3.SQLITE_READONLY: {},
	sqlite3.SQLITE_FULL:     {},
}

// classifyStoreOpenFailure maps storage faults to recovery mode.
func classifyStoreOpenFailure(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrStoreIncompatible) {
		return err
	}
	var se *sqlite.Error
	if errors.As(err, &se) {
		if _, unservable := unservableStoreCodes[se.Code()&0xff]; unservable {
			return storeIncompatible(RecoveryReasonIntegrityFailed, 0, err.Error())
		}
		return err
	}
	if errors.Is(err, fs.ErrPermission) {
		return storeIncompatible(RecoveryReasonIntegrityFailed, 0, err.Error())
	}
	return err
}

// Immediate transactions acquire the write lock before reads.
func openWriter(ctx context.Context, dbPath string, opts StoreOptions) (*sql.DB, error) {
	query := writerDSNQuery
	if opts.FastTestSync {
		query = "_txlock=immediate&_pragma=foreign_keys(on)&_pragma=busy_timeout(10000)&_pragma=synchronous(normal)&_pragma=temp_store(memory)&_pragma=cache_size(-2000)"
	}
	dsn := fileDSN(dbPath, query)
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	if _, err := sqlDB.ExecContext(ctx, `PRAGMA auto_vacuum=INCREMENTAL`); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("configure incremental auto-vacuum: %w", err)
	}
	if _, err := sqlDB.ExecContext(ctx, `PRAGMA journal_mode=WAL`); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("configure wal journal: %w", err)
	}
	sqlDB.SetMaxOpenConns(writerOpenConns)
	sqlDB.SetMaxIdleConns(writerOpenConns)
	return sqlDB, nil
}

func fileDSN(dbPath, query string) string {
	path := url.URL{Path: filepath.ToSlash(dbPath)}
	return "file:" + path.EscapedPath() + "?" + query
}

func recordAppVersion(ctx context.Context, sqlDB Handle, created bool) error {
	current := runningAppVersion
	if created {
		return RecordAppVersionTransition(ctx, sqlDB, current, "", false)
	}
	previous, ok, err := ReadAppVersion(ctx, sqlDB)
	if err != nil {
		return err
	}
	if !ok {
		return RecordAppVersionTransition(ctx, sqlDB, current, "", false)
	}
	if previous == current {
		if _, transitionPending, err := ReadBootPreviousAppVersion(ctx, sqlDB); err != nil {
			return err
		} else if transitionPending {
			return RecordAppVersionTransition(ctx, sqlDB, current, "", false)
		}
		return nil
	}
	return RecordAppVersionTransition(ctx, sqlDB, current, previous, true)
}
