package sourcecatalog

import (
	"context"
	"database/sql"
	"errors"
	"sort"

	"github.com/lycaon/lycaon/internal/pagedview"
	_ "modernc.org/sqlite"
)

const structuralDirectoryWorkspaceMemory = 32 << 20
const (
	directoryDirty uint64 = 1 << iota
	directoryBranchesKnown
	directoryHasBranches
	directoryAncestor
	directoryStarted
	directoryRepair
	directoryCoverageQueued
)

type structuralDirectoryWork struct {
	value       structuralDirectory
	previous    structuralDirectory
	flags       uint64
	override    int
	hasPrevious bool
}

type structuralDirectoryWorkspace struct {
	dir                string
	base               structuralDirectoryIndex
	memory             map[string]structuralDirectoryWork
	bytes, memoryLimit int64
	hot                *pagedview.Cache[string, cachedDirectoryWork]
	statements         map[string]*sql.Stmt
	db                 *sql.DB
	tx                 *sql.Tx
	count              int
	revisions          uint64
	cachedRevision     uint64
	cached             structuralDirectoryIndex
	cachedValid        bool
}

func newStructuralDirectoryWorkspace(dir string, base structuralDirectoryIndex) (*structuralDirectoryWorkspace, error) {
	return &structuralDirectoryWorkspace{dir: dir, base: base, memory: make(map[string]structuralDirectoryWork), memoryLimit: structuralDirectoryWorkspaceMemory, count: base.Len()}, nil
}
func (w *structuralDirectoryWorkspace) Len() int { return w.count }
func (w *structuralDirectoryWorkspace) Close() error {
	var failures []error
	for _, statement := range w.statements {
		failures = append(failures, statement.Close())
	}
	w.statements = nil
	w.hot = nil
	if w.tx != nil {
		failures = append(failures, w.tx.Rollback())
		w.tx = nil
	}
	if w.db != nil {
		failures = append(failures, w.db.Close())
		w.db = nil
	}
	return errors.Join(failures...)
}
func directoryWorkBytes(key string, entry structuralDirectoryWork) int64 {
	return 512 + int64(2*len(key)+len(entry.value.observation.Failure)+len(entry.previous.observation.Path)+len(entry.previous.observation.Failure)+len(entry.value.observation.Epoch.BootID)+len(entry.previous.observation.Epoch.BootID))
}
func (w *structuralDirectoryWorkspace) read(ctx context.Context, key string) (structuralDirectoryWork, bool, error) {
	if err := ctx.Err(); err != nil {
		return structuralDirectoryWork{}, false, err
	}
	if w.tx == nil {
		value, found := w.memory[key]
		return value, found, nil
	}
	if entry, found, known := w.cachedWork(key); known {
		return entry, found, nil
	}
	statement, err := w.statement(ctx, "SELECT override,value,flags,previous FROM directories WHERE path=?") //nolint:sqlclosecheck // Workspace.Close releases this reusable statement.
	if err != nil {
		return structuralDirectoryWork{}, false, err
	}
	var value, previous []byte
	var entry structuralDirectoryWork
	err = statement.QueryRowContext(ctx, []byte(key)).Scan(&entry.override, &value, &entry.flags, &previous)
	if errors.Is(err, sql.ErrNoRows) {
		w.cacheWork(key, entry, false)
		return entry, false, nil
	}
	if err != nil {
		return entry, false, err
	}
	if len(value) > 0 {
		entry.value, err = decodeDirectoryWorkRecord(key, value)
		if err != nil {
			return entry, false, err
		}
	}
	if len(previous) > 0 {
		entry.previous, err = decodeDirectoryWorkRecord(key, previous)
		entry.hasPrevious = err == nil
	}
	if err == nil {
		w.cacheWork(key, entry, true)
	}
	return entry, true, err
}
func (w *structuralDirectoryWorkspace) write(ctx context.Context, key string, entry structuralDirectoryWork) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if w.tx == nil {
		old, found := w.memory[key]
		size := w.bytes + directoryWorkBytes(key, entry)
		if found {
			size -= directoryWorkBytes(key, old)
		}
		if size <= w.memoryLimit {
			w.memory[key] = entry
			w.bytes = size
			return nil
		}
		if err := w.spill(ctx); err != nil {
			return err
		}
	}
	return w.writeSQL(ctx, key, entry)
}
func (w *structuralDirectoryWorkspace) writeSQL(ctx context.Context, key string, entry structuralDirectoryWork) error {
	var value, previous []byte
	var err error
	if entry.override == 1 {
		value, err = encodeDirectoryWorkRecord(entry.value)
		if err != nil {
			return err
		}
	}
	if entry.hasPrevious {
		previous, err = encodeDirectoryWorkRecord(entry.previous)
		if err != nil {
			return err
		}
	}
	statement, err := w.statement(ctx, "INSERT INTO directories(path,override,value,flags,previous) VALUES(?,?,?,?,?) ON CONFLICT(path) DO UPDATE SET override=excluded.override,value=excluded.value,flags=excluded.flags,previous=excluded.previous") //nolint:sqlclosecheck // Workspace.Close releases this reusable statement.
	if err != nil {
		return err
	}
	_, err = statement.ExecContext(ctx, []byte(key), entry.override, value, entry.flags, previous)
	if err == nil {
		w.cacheWork(key, entry, true)
	}
	return err
}
func (w *structuralDirectoryWorkspace) spill(ctx context.Context) error {
	var err error
	// An empty SQLite filename creates a private disk-backed temporary database.
	w.db, err = sql.Open("sqlite", "")
	if err != nil {
		return err
	}
	w.db.SetMaxOpenConns(1)
	for _, statement := range []string{"PRAGMA journal_mode=OFF", "PRAGMA synchronous=OFF", "PRAGMA temp_store=FILE", "PRAGMA cache_size=-4096", "CREATE TABLE directories(path BLOB PRIMARY KEY,override INTEGER NOT NULL,value BLOB,flags INTEGER NOT NULL,previous BLOB) WITHOUT ROWID"} {
		if _, err = w.db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	w.tx, err = w.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	w.statements = make(map[string]*sql.Stmt)
	for key, entry := range w.memory {
		if err := w.writeSQL(ctx, key, entry); err != nil {
			return err
		}
	}
	w.memory = nil
	w.bytes = 0
	return nil
}
func (w *structuralDirectoryWorkspace) Get(ctx context.Context, key string) (structuralDirectory, bool, error) {
	entry, found, err := w.read(ctx, key)
	if err != nil {
		return structuralDirectory{}, false, err
	}
	if found && entry.override != 0 {
		return entry.value, entry.override == 1, nil
	}
	return w.base.Get(ctx, key)
}
func (w *structuralDirectoryWorkspace) Set(ctx context.Context, key string, value structuralDirectory) error {
	value.observation.Path = key
	value.observation.Observed = value.observation.Observed.UTC()
	value.observation.baseSequence = 0
	if err := validateDirectoryRecord(value); err != nil {
		return err
	}
	entry, _, err := w.read(ctx, key)
	if err != nil {
		return err
	}
	previous, found := entry.value, entry.override == 1
	if entry.override == 0 {
		previous, found, err = w.base.Get(ctx, key)
	}
	if err != nil {
		return err
	}
	if found && previous == value {
		return nil
	}
	entry.value = value
	entry.override = 1
	if err := w.write(ctx, key, entry); err != nil {
		return err
	}
	if !found {
		w.count++
	}
	w.revisions++
	return nil
}
func (w *structuralDirectoryWorkspace) delete(ctx context.Context, key string) error {
	_, found, err := w.Get(ctx, key)
	if err != nil || !found {
		return err
	}
	entry, _, err := w.read(ctx, key)
	if err != nil {
		return err
	}
	entry.value = structuralDirectory{}
	entry.override = 2
	if err := w.write(ctx, key, entry); err != nil {
		return err
	}
	w.count--
	w.revisions++
	return nil
}
func (w *structuralDirectoryWorkspace) Flags(ctx context.Context, key string) (uint64, error) {
	entry, _, err := w.read(ctx, key)
	return entry.flags, err
}
func (w *structuralDirectoryWorkspace) UpdateFlags(ctx context.Context, key string, set, clear uint64) error {
	if set&directoryDirty != 0 {
		set |= directoryRepair
	}
	entry, found, err := w.read(ctx, key)
	if err != nil {
		return err
	}
	flags := (entry.flags | set) &^ clear
	if entry.flags == flags {
		return nil
	}
	entry.flags = flags
	if w.tx == nil {
		return w.write(ctx, key, entry)
	}
	statement, err := w.statement(ctx, "INSERT INTO directories(path,override,flags) VALUES(?,0,?) ON CONFLICT(path) DO UPDATE SET flags=excluded.flags") //nolint:sqlclosecheck // Workspace.Close releases this reusable statement.
	if err != nil {
		return err
	}
	_, err = statement.ExecContext(ctx, []byte(key), flags)
	if err == nil {
		w.cacheWork(key, entry, found || flags != 0)
	}
	return err
}
func (w *structuralDirectoryWorkspace) Previous(ctx context.Context, key string) (structuralDirectory, bool, error) {
	entry, _, err := w.read(ctx, key)
	return entry.previous, entry.hasPrevious, err
}
func (w *structuralDirectoryWorkspace) SetPrevious(ctx context.Context, key string, value structuralDirectory) error {
	entry, _, err := w.read(ctx, key)
	if err != nil {
		return err
	}
	entry.previous = value
	entry.hasPrevious = true
	return w.write(ctx, key, entry)
}
func (w *structuralDirectoryWorkspace) VisitFlags(ctx context.Context, mask uint64, reverse bool, visit func(string, uint64) error) error {
	return w.visitWork(ctx, mask, reverse, func(key string, entry structuralDirectoryWork) error { return visit(key, entry.flags) })
}
func (w *structuralDirectoryWorkspace) VisitFlagKeys(ctx context.Context, mask uint64, reverse bool, visit func(string, uint64) error) error {
	if w.tx == nil {
		return w.VisitFlags(ctx, mask, reverse, visit)
	}
	cursor := directoryWorkCursor{workspace: w, mask: mask, reverse: reverse, flagsOnly: true}
	for {
		key, entry, found, err := cursor.next(ctx)
		if err != nil || !found {
			return err
		}
		if err := visit(key, entry.flags); err != nil {
			return err
		}
	}
}
func (w *structuralDirectoryWorkspace) visitWork(ctx context.Context, mask uint64, reverse bool, visit func(string, structuralDirectoryWork) error) error {
	if w.tx == nil {
		keys := make([]string, 0, len(w.memory))
		for key, entry := range w.memory {
			if mask == 0 || entry.flags&mask != 0 {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		for i := range keys {
			if err := ctx.Err(); err != nil {
				return err
			}
			at := i
			if reverse {
				at = len(keys) - 1 - i
			}
			entry, _, err := w.read(ctx, keys[at])
			if err != nil {
				return err
			}
			if err := visit(keys[at], entry); err != nil {
				return err
			}
		}
		return nil
	}
	cursor := directoryWorkCursor{workspace: w, mask: mask, reverse: reverse}
	for {
		key, entry, found, err := cursor.next(ctx)
		if err != nil || !found {
			return err
		}
		if err := visit(key, entry); err != nil {
			return err
		}
	}
}
