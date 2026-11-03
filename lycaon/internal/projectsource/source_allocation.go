package projectsource

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"io/fs"
	"math"

	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/sourceeffect"
)

// Only multi-link entries need an exact set. SQLite keeps that set on private
// temporary storage with a fixed cache, independently of the durable journal.
type sourceTreeAllocation struct {
	memory        sourceeffect.Memory
	releaseMemory func()
	bytes         int64
	known         bool
	db            *sql.DB
	tx            *sql.Tx
}

func (s *sourceTreeAllocation) observe(ctx context.Context, info fs.FileInfo) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !s.known {
		return nil
	}
	facts := fspath.EntryAllocation(info)
	if !facts.Known {
		s.known = false
		return nil
	}
	if !info.IsDir() && facts.Links > 1 {
		if s.releaseMemory == nil {
			release, err := s.memory.Reserve(ctx, (1<<20)+(64<<10))
			if err != nil {
				return err
			}
			s.releaseMemory = release
		}
		inserted, err := s.observeLink(ctx, facts.Identity)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			s.known = false
			return nil
		}
		if !inserted {
			return nil
		}
	}
	if facts.Bytes > math.MaxInt64-s.bytes {
		s.known = false
		return nil
	}
	s.bytes += facts.Bytes
	return nil
}

func (s *sourceTreeAllocation) observeLink(ctx context.Context, identity fspath.AllocationIdentity) (bool, error) {
	if s.db == nil {
		database, err := sql.Open("sqlite", "")
		if err != nil {
			return false, err
		}
		s.db = database
		database.SetMaxOpenConns(1)
		database.SetMaxIdleConns(1)
		_, err = database.ExecContext(ctx, `PRAGMA cache_size=-1024; PRAGMA temp_store=FILE; PRAGMA journal_mode=OFF; PRAGMA synchronous=OFF;
CREATE TABLE linked_entries (identity BLOB PRIMARY KEY) WITHOUT ROWID;`)
		if err != nil {
			return false, err
		}
		s.tx, err = database.BeginTx(ctx, nil)
		if err != nil {
			return false, err
		}
	}
	var key [16]byte
	binary.LittleEndian.PutUint64(key[:8], identity.Device)
	binary.LittleEndian.PutUint64(key[8:], identity.Inode)
	result, err := s.tx.ExecContext(ctx, `INSERT OR IGNORE INTO linked_entries(identity) VALUES(?)`, key[:])
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count == 1, err
}

func (s *sourceTreeAllocation) close() error {
	if s.releaseMemory != nil {
		defer s.releaseMemory()
		s.releaseMemory = nil
	}
	var rollback error
	if s.tx != nil {
		rollback = s.tx.Rollback()
		if errors.Is(rollback, sql.ErrTxDone) {
			rollback = nil
		}
		s.tx = nil
	}
	if s.db == nil {
		return rollback
	}
	err := s.db.Close()
	s.db = nil
	return errors.Join(rollback, err)
}
