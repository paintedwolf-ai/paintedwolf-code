package sourcecatalog

import (
	"context"
	"database/sql"
	"errors"
	"sync"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/pagedview"
)

const projectionSchema = `
CREATE TABLE IF NOT EXISTS projection_meta (
 id TEXT PRIMARY KEY, rows INTEGER NOT NULL DEFAULT 0
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS projection_rows (
 projection TEXT NOT NULL, rank INTEGER NOT NULL, address TEXT NOT NULL,
 parent TEXT NOT NULL, directory INTEGER NOT NULL, ancillary INTEGER NOT NULL,
 subtree_end INTEGER NOT NULL, body BLOB NOT NULL,
 PRIMARY KEY(projection,rank)
) WITHOUT ROWID;
CREATE UNIQUE INDEX IF NOT EXISTS projection_address ON projection_rows(projection,address) WHERE ancillary=0;
`

// ProjectionRecord retains one immutable presentation row. Rank and subtree end
// permit distant viewport reads and sticky ancestors without a prefix scan.
type ProjectionRecord struct {
	Rank                 int64
	Address, Parent      string
	Directory, Ancillary bool
	End                  int64
	Body                 []byte
}

// ProjectionRows holds an optional materialized projection in the shared index.
// The builder writes bounded batches and publishes only after it finishes.
type ProjectionRows struct {
	db      *sql.DB
	store   *indexStore
	id      string
	release func()
	once    sync.Once
}

func (c *Directories) NewProjectionRows(ctx context.Context, projectID string, root Root) (*ProjectionRows, error) {
	store, err := c.trees.indexStore(ctx, projectID, root)
	if err != nil {
		return nil, err
	}
	return newProjectionRows(ctx, store)
}

func newProjectionRows(ctx context.Context, store *indexStore) (*ProjectionRows, error) {
	db, release, err := store.stores.Directories.acquirePresentation(ctx)
	if err != nil {
		return nil, err
	}
	unwrite, err := store.writer.Write(ctx, store.writable)
	if err != nil {
		release()
		return nil, err
	}
	defer unwrite()
	id := uuid.NewString()
	if _, err := db.ExecContext(ctx, "INSERT INTO projection_meta(id) VALUES(?)", id); err != nil {
		release()
		return nil, err
	}
	store.mu.Lock()
	store.projections++
	store.mu.Unlock()
	return &ProjectionRows{db: db, store: store, id: id, release: func() {
		store.mu.Lock()
		store.projections--
		store.mu.Unlock()
		release()
	}}, nil
}

func (p *ProjectionRows) Write(ctx context.Context, rows []ProjectionRecord, ends map[int64]int64) error {
	if len(rows) > pagedview.MaxRows || len(ends) > pagedview.MaxRows {
		return pagedview.ErrRange
	}
	unwrite, err := p.store.writer.Write(ctx, p.store.writable)
	if err != nil {
		return err
	}
	defer unwrite()
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var count int64
	if err := tx.QueryRowContext(ctx, "SELECT rows FROM projection_meta WHERE id=?", p.id).Scan(&count); err != nil {
		return err
	}
	for i, row := range rows {
		if row.Rank != count+int64(i) || row.End < row.Rank+1 || len(row.Body) > pagedview.MaxFrameBytes {
			return pagedview.ErrRange
		}
	}
	for _, row := range rows {
		if _, err := tx.ExecContext(ctx, `INSERT INTO projection_rows(projection,rank,address,parent,directory,ancillary,subtree_end,body)
   VALUES(?,?,?,?,?,?,?,?)`, p.id, row.Rank, row.Address, row.Parent, row.Directory, row.Ancillary, row.End, row.Body); err != nil {
			return err
		}
	}
	for rank, end := range ends {
		if end < rank+1 || end > count+int64(len(rows)) {
			return pagedview.ErrRange
		}
		if _, err := tx.ExecContext(ctx, "UPDATE projection_rows SET subtree_end=? WHERE projection=? AND rank=? AND directory=1", end, p.id, rank); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, "UPDATE projection_meta SET rows=? WHERE id=?", count+int64(len(rows)), p.id); err != nil {
		return err
	}
	return tx.Commit()
}

func (p *ProjectionRows) Frame(ctx context.Context, offset int64, limit int) ([]ProjectionRecord, error) {
	if offset < 0 || limit < 1 || limit > pagedview.MaxRows {
		return nil, pagedview.ErrRange
	}
	rows, err := p.db.QueryContext(ctx, `SELECT rank,address,parent,directory,ancillary,subtree_end,body FROM projection_rows
  WHERE projection=? AND rank>=? ORDER BY rank LIMIT ?`, p.id, offset, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]ProjectionRecord, 0, limit)
	for rows.Next() {
		var record ProjectionRecord
		if err := rows.Scan(&record.Rank, &record.Address, &record.Parent, &record.Directory, &record.Ancillary, &record.End, &record.Body); err != nil {
			return nil, err
		}
		out = append(out, record)
	}
	return out, rows.Err()
}

func (p *ProjectionRows) Locate(ctx context.Context, address string) (ProjectionRecord, error) {
	var row ProjectionRecord
	err := p.db.QueryRowContext(ctx, `SELECT rank,address,parent,directory,ancillary,subtree_end,body FROM projection_rows
  WHERE projection=? AND address=? AND ancillary=0`, p.id, address).
		Scan(&row.Rank, &row.Address, &row.Parent, &row.Directory, &row.Ancillary, &row.End, &row.Body)
	if errors.Is(err, sql.ErrNoRows) {
		err = pagedview.ErrMissing
	}
	return row, err
}

func (p *ProjectionRows) Count(ctx context.Context) (int64, error) {
	var count int64
	err := p.db.QueryRowContext(ctx, "SELECT rows FROM projection_meta WHERE id=?", p.id).Scan(&count)
	return count, err
}

func (p *ProjectionRows) Release(ctx context.Context) error {
	for {
		unwrite, err := p.store.writer.Write(ctx, p.store.writable)
		if err != nil {
			return err
		}
		result, err := p.db.ExecContext(ctx, `DELETE FROM projection_rows WHERE projection=? AND rank IN
   (SELECT rank FROM projection_rows WHERE projection=? ORDER BY rank LIMIT 1000)`, p.id, p.id)
		unwrite()
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count == 0 {
			break
		}
	}
	unwrite, err := p.store.writer.Write(ctx, p.store.writable)
	if err != nil {
		return err
	}
	defer unwrite()
	_, err = p.db.ExecContext(ctx, "DELETE FROM projection_meta WHERE id=?", p.id)
	return err
}
func (p *ProjectionRows) Close() { p.once.Do(p.release) }
