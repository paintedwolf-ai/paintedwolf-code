package sourcecatalog

import (
	"context"
	"database/sql"
	"errors"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/repochange"
)

const treeOverlayCleanupTimeout = 30 * time.Second

const treeOverlaySchema = `
CREATE TABLE IF NOT EXISTS tree_overlay_nodes (
 projection TEXT NOT NULL, path TEXT NOT NULL, parent TEXT NOT NULL, depth INTEGER NOT NULL,
 directory INTEGER NOT NULL, visible INTEGER NOT NULL DEFAULT 0, virtual INTEGER NOT NULL DEFAULT 0,
 weight INTEGER NOT NULL DEFAULT 0, suppress_empty INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY(projection,path)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS tree_overlay_depth ON tree_overlay_nodes(projection,depth,path);
`

type OverlayNode struct {
	Path, Parent                               string
	Depth                                      int
	Directory, Visible, Virtual, SuppressEmpty bool
	Weight                                     int64
}

// TreeOverlay stores sparse review paths and their projected additions. Its
// weighted pages are immutable after publication and need no retained read transaction.
type TreeOverlay struct{ rows *ProjectionRows }

func (c *Catalog) NewTreeOverlay(ctx context.Context, projectID string, root Root) (*TreeOverlay, error) {
	rows, err := c.NewProjectionRows(ctx, projectID, root)
	if err != nil {
		return nil, err
	}
	return &TreeOverlay{rows: rows}, nil
}
func (o *TreeOverlay) ID() string { return o.rows.id }
func (o *TreeOverlay) Close()     { o.rows.Close() }

func (o *TreeOverlay) Add(ctx context.Context, nodes []OverlayNode) error {
	if len(nodes) > pagedview.MaxRows {
		return pagedview.ErrRange
	}
	unwrite, err := o.rows.store.write(ctx)
	if err != nil {
		return err
	}
	defer unwrite()
	tx, err := o.rows.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, node := range nodes {
		if node.Path == "" || node.Path != path.Clean(node.Path) || strings.HasPrefix(node.Path, "/") || node.Path == ".." || strings.HasPrefix(node.Path, "../") || node.Weight < 0 {
			return pagedview.ErrRange
		}
		if repochange.IsPrivatePath(filepath.Join(o.rows.store.root.Path, filepath.FromSlash(node.Path))) {
			return pagedview.ErrRange
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO tree_overlay_nodes(projection,path,parent,depth,directory) VALUES(?,?,?,?,?)`, o.rows.id, node.Path, node.Parent, node.Depth, node.Directory); err != nil {
			return err
		}
		// A path that contains another deleted path presents as a virtual directory.
		if node.Directory {
			if _, err := tx.ExecContext(ctx, "UPDATE tree_overlay_nodes SET directory=1 WHERE projection=? AND path=?", o.rows.id, node.Path); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func scanOverlayNode(row interface{ Scan(...any) error }) (OverlayNode, error) {
	var node OverlayNode
	err := row.Scan(&node.Path, &node.Parent, &node.Depth, &node.Directory, &node.Visible, &node.Virtual, &node.Weight, &node.SuppressEmpty)
	return node, err
}

const overlayColumns = "path,parent,depth,directory,visible,virtual,weight,suppress_empty"

func (o *TreeOverlay) Node(ctx context.Context, rel string) (OverlayNode, error) {
	node, err := scanOverlayNode(o.rows.db.QueryRowContext(ctx, "SELECT "+overlayColumns+" FROM tree_overlay_nodes WHERE projection=? AND path=?", o.rows.id, rel))
	if errors.Is(err, sql.ErrNoRows) {
		err = pagedview.ErrMissing
	}
	return node, err
}

// Level gives the builder bounded parent-first or child-first batches.
func (o *TreeOverlay) Level(ctx context.Context, depth int, after string) ([]OverlayNode, error) {
	rows, err := o.rows.db.QueryContext(ctx, "SELECT "+overlayColumns+" FROM tree_overlay_nodes WHERE projection=? AND depth=? AND path>? ORDER BY path LIMIT ?", o.rows.id, depth, after, pagedview.MaxRows)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	nodes := make([]OverlayNode, 0, pagedview.MaxRows)
	for rows.Next() {
		node, err := scanOverlayNode(rows)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, node)
	}
	return nodes, rows.Err()
}
func (o *TreeOverlay) Depth(ctx context.Context) (int, error) {
	var depth int
	err := o.rows.db.QueryRowContext(ctx, "SELECT COALESCE(MAX(depth),0) FROM tree_overlay_nodes WHERE projection=?", o.rows.id).Scan(&depth)
	return depth, err
}

func (o *TreeOverlay) Classify(ctx context.Context, nodes []OverlayNode) error {
	return o.change(ctx, nodes, false)
}
func (o *TreeOverlay) Weigh(ctx context.Context, nodes []OverlayNode) error {
	return o.change(ctx, nodes, true)
}
func (o *TreeOverlay) change(ctx context.Context, nodes []OverlayNode, weights bool) error {
	if len(nodes) > pagedview.MaxRows {
		return pagedview.ErrRange
	}
	unwrite, err := o.rows.store.write(ctx)
	if err != nil {
		return err
	}
	defer unwrite()
	tx, err := o.rows.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, node := range nodes {
		if node.Weight < 0 {
			return pagedview.ErrWeight
		}
		if _, err := tx.ExecContext(ctx, "UPDATE tree_overlay_nodes SET visible=?,virtual=?,weight=?,suppress_empty=? WHERE projection=? AND path=?", node.Visible, node.Virtual, node.Weight, node.SuppressEmpty, o.rows.id, node.Path); err != nil {
			return err
		}
		if weights && node.Visible && node.Path != "." {
			// Overlay pages are private until publication, so one generation suffices.
			pages := newRangeWriter(tx, nil)
			index, _, err := pages.Open(ctx, o.rows.id, node.Parent)
			if err != nil {
				return err
			}
			key := OverlayOrder(node)
			if err := index.Set(ctx, pagedview.RangeItem[TreeItem]{Key: key, Value: TreeItem{Path: node.Path}, Weight: node.Weight}); err != nil {
				return err
			}
			if err := pages.Save(ctx, o.rows.id, node.Parent, index, DirectoryState{}); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func OverlayOrder(node OverlayNode) string {
	key := DirectoryOrder(path.Base(node.Path), node.Directory)
	if node.Virtual {
		return "2" + key
	}
	return key
}

func (o *TreeOverlay) Children(ctx context.Context, dir string) (*pagedview.RangeIndex[TreeItem], error) {
	index, _, err := newRangeReader(o.rows.db, nil).Open(ctx, o.rows.id, dir)
	return index, err
}

func (o *TreeOverlay) Release(ctx context.Context) error {
	for _, table := range []string{"tree_overlay_nodes", "range_pages", "range_roots"} {
		if err := o.deleteBatches(ctx, table); err != nil {
			return err
		}
	}
	return o.rows.Release(ctx)
}
func (o *TreeOverlay) deleteBatches(ctx context.Context, table string) error {
	var statement string
	switch table {
	case "tree_overlay_nodes":
		statement = "DELETE FROM tree_overlay_nodes WHERE projection=? AND path IN (SELECT path FROM tree_overlay_nodes WHERE projection=? LIMIT 1000)"
	case "range_pages":
		statement = "DELETE FROM range_pages WHERE scope=? AND id IN (SELECT id FROM range_pages WHERE scope=? LIMIT 1000)"
	case "range_roots":
		statement = "DELETE FROM range_roots WHERE scope=? AND name IN (SELECT name FROM range_roots WHERE scope=? LIMIT 1000)"
	default:
		return pagedview.ErrRange
	}
	for {
		unwrite, err := o.rows.store.write(ctx)
		if err != nil {
			return err
		}
		result, err := o.rows.db.ExecContext(ctx, statement, o.rows.id, o.rows.id)
		unwrite()
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil || count == 0 {
			return err
		}
	}
}
