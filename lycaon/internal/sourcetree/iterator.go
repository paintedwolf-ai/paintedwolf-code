package sourcetree

import (
	"context"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
)

type treeCursorLevel struct {
	row             Row
	next, end       int64
	marker, message string
	prepared        bool
}

// frameRows seeks once, then retains its ancestor stack while advancing siblings.
func (p *Projection) frameRows(ctx context.Context, offset int64, limit int, total int64) ([]Row, error) {
	root := Row{Address: Address{Root: p.Root, Path: "."}, Kind: "directory", Expanded: p.Rules.At(Address{Root: p.Root, Path: "."}).Open}
	stack := []treeCursorLevel{{row: root, next: offset, end: total}}
	rows := make([]Row, 0, limit)
	for len(stack) > 0 && len(rows) < limit {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		current := &stack[len(stack)-1]
		if current.next >= current.end {
			stack = stack[:len(stack)-1]
			continue
		}
		if current.next == 0 {
			rows = append(rows, current.row)
			current.next++
			continue
		}
		if !current.prepared {
			var err error
			current.marker, current.message, err = p.ancillary(ctx, current.row.Address.Path)
			if err != nil {
				return nil, err
			}
			current.prepared = true
		}
		rank := current.next - 1
		if current.marker != "" {
			if rank == 0 {
				row := current.row
				row.Depth++
				row.Kind = current.marker
				row.Error = current.message
				row.Expanded = false
				rows = append(rows, row)
				current.next++
				continue
			}
			rank--
		}
		entry, within, err := p.child(ctx, current.row.Address.Path, rank)
		if err != nil {
			return nil, err
		}
		row, weight, err := p.cursorRow(ctx, entry, current.row.Depth+1)
		if err != nil {
			return nil, err
		}
		current.next += weight - within
		stack = append(stack, treeCursorLevel{row: row, next: within, end: weight})
	}
	return rows, nil
}
func (p *Projection) cursorRow(ctx context.Context, entry sourcecatalog.Entry, depth int) (Row, int64, error) {
	row := Row{Address: Address{Root: p.Root, Path: entry.Path}, Name: entry.Name, Kind: "file", Depth: depth, Symlink: entry.IsSymlink}
	node, err := p.reviewNode(ctx, entry.Path)
	if err != nil {
		return row, 0, err
	}
	row.Deleted = node.Visible && node.Virtual
	weight := int64(1)
	if entry.IsDir {
		row.Kind = "directory"
		row.Expanded, err = p.open(ctx, entry.Path)
		if err != nil {
			return row, 0, err
		}
		if row.Expanded {
			nested, _, err := p.children(ctx, entry.Path)
			if err != nil {
				return row, 0, err
			}
			weight += nested
		}
	}
	return row, weight, nil
}
