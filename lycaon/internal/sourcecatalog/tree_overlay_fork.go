package sourcecatalog

import (
	"context"

	"github.com/lycaon/lycaon/internal/pagedview"
)

// Fork copies sparse facts in bounded transactions, leaving projected weights
// behind. An accepted disclosure gets independent immutable range pages.
func (o *TreeOverlay) Fork(ctx context.Context) (*TreeOverlay, error) {
	rows, err := newProjectionRows(ctx, o.rows.store)
	if err != nil {
		return nil, err
	}
	next := &TreeOverlay{rows: rows}
	after := ""
	for {
		last, count, err := o.copyFactBatch(ctx, next, after)
		if err != nil {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), treeOverlayCleanupTimeout)
			_ = next.Release(cleanup)
			cancel()
			next.Close()
			return nil, err
		}
		if count == 0 {
			return next, nil
		}
		after = last
	}
}

func (o *TreeOverlay) copyFactBatch(ctx context.Context, next *TreeOverlay, after string) (string, int64, error) {
	unwrite, err := o.rows.store.write(ctx)
	if err != nil {
		return "", 0, err
	}
	defer unwrite()
	tx, err := o.rows.db.BeginTx(ctx, nil)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `INSERT INTO tree_overlay_nodes(projection,path,parent,depth,directory)
 SELECT ?,path,parent,depth,directory FROM tree_overlay_nodes WHERE projection=? AND path>? ORDER BY path LIMIT ?`, next.rows.id, o.rows.id, after, pagedview.MaxRows)
	if err != nil {
		return "", 0, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return "", 0, err
	}
	if count == 0 {
		return "", 0, nil
	}
	var last string
	if err := tx.QueryRowContext(ctx, "SELECT path FROM tree_overlay_nodes WHERE projection=? ORDER BY path DESC LIMIT 1", next.rows.id).Scan(&last); err != nil {
		return "", 0, err
	}
	return last, count, tx.Commit()
}
