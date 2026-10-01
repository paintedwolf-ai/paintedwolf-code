package sourcecatalog

import (
	"context"
	"database/sql"
	"errors"
	"math"

	"github.com/lycaon/lycaon/internal/pagedview"
)

// Range pages are private mutable storage for one ephemeral projection. Source
// structure generations use structuralSegments instead.
const rangeSchema = `
CREATE TABLE IF NOT EXISTS range_pages (
 id INTEGER PRIMARY KEY AUTOINCREMENT, scope TEXT NOT NULL, name TEXT NOT NULL, body BLOB NOT NULL);
CREATE INDEX IF NOT EXISTS range_page_owner ON range_pages(scope,name,id);
CREATE TABLE IF NOT EXISTS range_roots (
 scope TEXT NOT NULL, name TEXT NOT NULL, page INTEGER NOT NULL,
 listed INTEGER NOT NULL, complete INTEGER NOT NULL, failure TEXT NOT NULL,
 PRIMARY KEY(scope,name)) WITHOUT ROWID;
`

const headGeneration = math.MaxInt64

type TreeItem struct {
	Path     string
	Symlink  bool
	Sequence int64
}

type DirectoryState struct {
	Listed   bool
	Complete bool
	Failure  string
}

func stateOf(observation DirectoryObservation) DirectoryState {
	return DirectoryState{Listed: observation.FirstListed > 0, Complete: observation.Complete, Failure: observation.Failure}
}

type rangeTx interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

type RangePages struct {
	tx          rangeTx
	fresh       map[uint64]struct{}
	scope, name string
	cache       *pagedview.Cache[uint64, pagedview.RangePage[TreeItem]]
}

func newRangeReader(tx rangeTx, cache *pagedview.Cache[uint64, pagedview.RangePage[TreeItem]]) RangePages {
	return RangePages{tx: tx, cache: cache}
}

func newRangeWriter(tx rangeTx, cache *pagedview.Cache[uint64, pagedview.RangePage[TreeItem]]) RangePages {
	return RangePages{tx: tx, fresh: make(map[uint64]struct{}), cache: cache}
}

func (s RangePages) Read(ctx context.Context, id uint64) (pagedview.RangePage[TreeItem], error) {
	if s.cache != nil {
		if page, ok := s.cache.Get(id); ok {
			return page, nil
		}
	}
	var page pagedview.RangePage[TreeItem]
	var body []byte
	err := s.tx.QueryRowContext(ctx, "SELECT body FROM range_pages WHERE id=?", id).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return page, pagedview.ErrMissing
	}
	if err != nil {
		return page, err
	}
	page, err = decodeRangePage(body)
	if err == nil && s.cache != nil {
		s.cache.Put(id, page, rangePageBytes(page))
	}
	return page, err
}

func (s RangePages) Write(ctx context.Context, id uint64, page pagedview.RangePage[TreeItem]) (uint64, error) {
	if s.fresh == nil {
		return 0, errors.New("range pages opened for reading cannot publish")
	}
	body, err := encodeRangePage(page)
	if err != nil {
		return 0, err
	}
	if _, ok := s.fresh[id]; ok {
		if _, err := s.tx.ExecContext(ctx, "UPDATE range_pages SET body=? WHERE id=?", body, id); err != nil {
			return 0, err
		}
		s.remember(id, page)
		return id, nil
	}
	var created uint64
	if err := s.tx.QueryRowContext(ctx, "INSERT INTO range_pages(scope,name,body) VALUES(?,?,?) RETURNING id", s.scope, s.name, body).Scan(&created); err != nil {
		return 0, err
	}
	if id != 0 {
		if _, err := s.tx.ExecContext(ctx, "DELETE FROM range_pages WHERE id=?", id); err != nil {
			return 0, err
		}
	}
	s.fresh[created] = struct{}{}
	s.remember(created, page)
	return created, nil
}

func (s RangePages) remember(id uint64, page pagedview.RangePage[TreeItem]) {
	if s.cache != nil {
		s.cache.Put(id, page, rangePageBytes(page))
	}
}

func (s RangePages) Delete(ctx context.Context, id uint64) error {
	if s.fresh == nil {
		return errors.New("range pages opened for reading cannot publish")
	}
	delete(s.fresh, id)
	if s.cache != nil {
		s.cache.Delete(id)
	}
	_, err := s.tx.ExecContext(ctx, "DELETE FROM range_pages WHERE id=?", id)
	return err
}

func (s RangePages) Open(ctx context.Context, scope, name string) (*pagedview.RangeIndex[TreeItem], DirectoryState, error) {
	s.scope, s.name = scope, name
	index := &pagedview.RangeIndex[TreeItem]{Store: s}
	var state DirectoryState
	err := s.tx.QueryRowContext(ctx, "SELECT page,listed,complete,failure FROM range_roots WHERE scope=? AND name=?", scope, name).
		Scan(&index.Root, &state.Listed, &state.Complete, &state.Failure)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return index, state, err
}

func (s RangePages) Save(ctx context.Context, scope, name string, index *pagedview.RangeIndex[TreeItem], state DirectoryState) error {
	if s.fresh == nil {
		return errors.New("range pages opened for reading cannot publish")
	}
	_, err := s.tx.ExecContext(ctx, `INSERT INTO range_roots(scope,name,page,listed,complete,failure) VALUES(?,?,?,?,?,?)
 ON CONFLICT(scope,name) DO UPDATE SET page=excluded.page,listed=excluded.listed,complete=excluded.complete,failure=excluded.failure`,
		scope, name, index.Root, state.Listed, state.Complete, state.Failure)
	return err
}
