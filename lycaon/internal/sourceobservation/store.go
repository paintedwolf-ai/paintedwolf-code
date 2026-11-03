// Package sourceobservation stores the rebuildable stat-to-digest index a
// capture reuses: one row per file the host has read, with the stat facts
// that vouch for the digest until the file changes.
package sourceobservation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/observation"
	_ "modernc.org/sqlite"
)

const schemaVersion = 3
const openLockShards = 32

var openLocks [openLockShards]sync.Mutex

const schema = `
CREATE TABLE IF NOT EXISTS observed_files (
    root_path TEXT NOT NULL,
    path TEXT NOT NULL,
    sha256 TEXT NOT NULL,
    git_oid TEXT NOT NULL,
    size INTEGER NOT NULL CHECK (size >= 0),
    mode INTEGER NOT NULL,
    modified_ns INTEGER NOT NULL,
    provenance TEXT NOT NULL DEFAULT 'server_fs_request',
    completeness TEXT NOT NULL DEFAULT 'exact',
    layer TEXT NOT NULL DEFAULT 'trunk',
    uncertain INTEGER NOT NULL DEFAULT 0,
    source_generation INTEGER NOT NULL DEFAULT 0,
    ino INTEGER NOT NULL DEFAULT 0,
    dev INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (root_path, path)
) STRICT, WITHOUT ROWID;
PRAGMA user_version = 3;
`
const selectFile = `
SELECT path, sha256, git_oid, size, mode, modified_ns,
       provenance, completeness, layer, uncertain, source_generation, ino, dev
FROM observed_files WHERE root_path = ? AND path = ?`

// File is one file's last verified digests, provenance, layer, and stat/inode facts.
type File struct {
	Path             string
	SHA256           string
	GitOID           string
	Size             int64
	Mode             int64
	ModifiedNS       int64
	Provenance       observation.ObservationProvenance
	Completeness     observation.AttributionCompleteness
	Layer            layeredfs.LayerSource
	Uncertain        bool
	SourceGeneration uint64
	Ino              uint64
	Dev              uint64
}

// Store is an explicitly rebuildable SQLite index separate from user history.
type Store struct {
	path string
	once sync.Once
	db   *sql.DB
	err  error
}

// New creates a lazy observation store at path.
func New(path string) *Store {
	return &Store{path: filepath.Clean(strings.TrimSpace(path))}
}

func (s *Store) open(ctx context.Context) (*sql.DB, error) {
	if s == nil || s.path == "" || s.path == "." {
		return nil, errors.New("source observation path is not configured")
	}
	s.once.Do(func() {
		lock := observationOpenLock(s.path)
		lock.Lock()
		defer lock.Unlock()
		s.db, s.err = open(context.WithoutCancel(ctx), s.path)
	})
	return s.db, s.err
}

func observationOpenLock(path string) *sync.Mutex {
	index := crc32.ChecksumIEEE([]byte(filepath.Clean(path))) % openLockShards
	return &openLocks[index]
}

func open(ctx context.Context, path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	fresh, err := freshObservationFile(path)
	if err != nil {
		return nil, err
	}
	database, err := openFile(path)
	if err != nil {
		return nil, err
	}
	var version int
	versionErr := database.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version)
	rebuild := versionErr != nil || (version != schemaVersion && (version != 0 || !fresh))
	if rebuild {
		if err := database.Close(); err != nil {
			return nil, err
		}
		if err := removeFiles(path); err != nil {
			return nil, err
		}
		database, err = openFile(path)
		if err != nil {
			return nil, err
		}
		version = 0
	}
	if version == 0 {
		if _, err := database.ExecContext(ctx, schema); err != nil {
			_ = database.Close()
			return nil, fmt.Errorf("create source observation cache: %w", err)
		}
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = database.Close()
		return nil, err
	}
	return database, nil
}

func openFile(path string) (*sql.DB, error) {
	database, err := sql.Open("sqlite", "file:"+path+"?_txlock=immediate&_pragma=busy_timeout(10000)")
	if err != nil {
		return nil, err
	}
	database.SetMaxOpenConns(1)
	return database, nil
}

func freshObservationFile(path string) (bool, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return info.Size() == 0, nil
}

func removeFiles(path string) error {
	for _, candidate := range []string{path, path + "-wal", path + "-shm", path + "-journal"} {
		if err := os.Remove(candidate); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// Get returns one path's observation, or false when none is held.
func (s *Store) Get(ctx context.Context, rootPath, path string) (File, bool, error) {
	database, err := s.open(ctx)
	if err != nil {
		return File{}, false, err
	}
	return scanFile(database.QueryRowContext(ctx, selectFile, rootPath, path))
}

// Reader answers many point lookups through one prepared statement, which
// is what a survey of a large root does.
type Reader struct {
	stmt *sql.Stmt
}

// Reader prepares the lookup a capture repeats for every file it meets.
func (s *Store) Reader(ctx context.Context) (*Reader, error) {
	database, err := s.open(ctx)
	if err != nil {
		return nil, err
	}
	stmt, err := database.PrepareContext(ctx, selectFile)
	if err != nil {
		return nil, err
	}
	return &Reader{stmt: stmt}, nil
}

// Get returns one path's observation, or false when none is held.
func (r *Reader) Get(ctx context.Context, rootPath, path string) (File, bool, error) {
	return scanFile(r.stmt.QueryRowContext(ctx, rootPath, path))
}

// Close releases the prepared statement.
func (r *Reader) Close() error {
	if r == nil || r.stmt == nil {
		return nil
	}
	return r.stmt.Close()
}

func scanFile(row *sql.Row) (File, bool, error) {
	var file File
	var prov, comp, layer string
	var uncertain int64
	var gen, ino, dev int64
	err := row.Scan(
		&file.Path, &file.SHA256, &file.GitOID, &file.Size, &file.Mode, &file.ModifiedNS,
		&prov, &comp, &layer, &uncertain, &gen, &ino, &dev,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return File{}, false, nil
	}
	if err != nil {
		return File{}, false, err
	}
	file.Provenance = observation.ObservationProvenance(prov)
	file.Completeness = observation.AttributionCompleteness(comp)
	file.Layer = layeredfs.LayerSource(layer)
	file.Uncertain = uncertain != 0
	file.SourceGeneration = uint64(gen)
	file.Ino = uint64(ino)
	file.Dev = uint64(dev)
	return file, true, nil
}

// ForEachPath streams one root's observed paths in byte order, so a survey
// can compare them with what it admitted without holding either set.
func (s *Store) ForEachPath(ctx context.Context, rootPath string, fn func(path string) error) error {
	database, err := s.open(ctx)
	if err != nil {
		return err
	}
	rows, err := database.QueryContext(ctx, `SELECT path FROM observed_files WHERE root_path = ? ORDER BY path`, rootPath)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return err
		}
		if err := fn(path); err != nil {
			return err
		}
	}
	return rows.Err()
}

// Put stores a batch captured from one root atomically.
func (s *Store) Put(ctx context.Context, rootPath string, files []File) error {
	if len(files) == 0 {
		return nil
	}
	database, err := s.open(ctx)
	if err != nil {
		return err
	}
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, file := range files {
		norm := normalizeFileDefaults(file)
		uncertainInt := int64(0)
		if norm.Uncertain {
			uncertainInt = 1
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO observed_files (root_path, path, sha256, git_oid, size, mode, modified_ns, provenance, completeness, layer, uncertain, source_generation, ino, dev)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(root_path, path) DO UPDATE SET
sha256=excluded.sha256, git_oid=excluded.git_oid, size=excluded.size, mode=excluded.mode, modified_ns=excluded.modified_ns,
provenance=excluded.provenance, completeness=excluded.completeness, layer=excluded.layer,
uncertain=excluded.uncertain, source_generation=excluded.source_generation, ino=excluded.ino, dev=excluded.dev
WHERE observed_files.layer != 'private' OR excluded.layer = 'private'`,
			rootPath, norm.Path, norm.SHA256, norm.GitOID, norm.Size, norm.Mode, norm.ModifiedNS,
			string(norm.Provenance), string(norm.Completeness), string(norm.Layer),
			uncertainInt, int64(norm.SourceGeneration), int64(norm.Ino), int64(norm.Dev)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DeletePaths removes paths that left the tree.
func (s *Store) DeletePaths(ctx context.Context, rootPath string, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	database, err := s.open(ctx)
	if err != nil {
		return err
	}
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, path := range paths {
		if _, err := tx.ExecContext(ctx, `DELETE FROM observed_files WHERE root_path = ? AND path = ?`, rootPath, path); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DeleteRoot removes a detached root's rebuildable observations.
func (s *Store) DeleteRoot(ctx context.Context, rootPath string) error {
	database, err := s.open(ctx)
	if err != nil {
		return err
	}
	_, err = database.ExecContext(ctx, `DELETE FROM observed_files WHERE root_path = ?`, rootPath)
	return err
}

// Clear removes every rebuildable observation and compacts the cache file.
func (s *Store) Clear(ctx context.Context) error {
	database, err := s.open(ctx)
	if err != nil {
		return err
	}
	if _, err := database.ExecContext(ctx, `DELETE FROM observed_files`); err != nil {
		return err
	}
	_, err = database.ExecContext(ctx, `VACUUM`)
	return err
}

// Close releases the cache database.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}
