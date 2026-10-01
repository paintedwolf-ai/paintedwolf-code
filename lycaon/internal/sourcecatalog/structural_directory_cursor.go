package sourcecatalog

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/pagedview"
)

type directoryWorkRow struct {
	key   string
	entry structuralDirectoryWork
}
type directoryWorkCursor struct {
	workspace *structuralDirectoryWorkspace
	mask      uint64
	flagsOnly bool
	reverse   bool
	started   bool
	after     string
	rows      []directoryWorkRow
	keys      []string
}

func (cursor *directoryWorkCursor) next(ctx context.Context) (string, structuralDirectoryWork, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", structuralDirectoryWork{}, false, err
	}
	if cursor.workspace.tx == nil {
		if !cursor.started {
			for key, entry := range cursor.workspace.memory {
				if cursor.mask == 0 || entry.flags&cursor.mask != 0 {
					cursor.keys = append(cursor.keys, key)
				}
			}
			sort.Strings(cursor.keys)
			cursor.started = true
		}
		if len(cursor.keys) == 0 {
			return "", structuralDirectoryWork{}, false, nil
		}
		at := 0
		if cursor.reverse {
			at = len(cursor.keys) - 1
		}
		key := cursor.keys[at]
		cursor.after = key
		if cursor.reverse {
			cursor.keys = cursor.keys[:at]
		} else {
			cursor.keys = cursor.keys[1:]
		}
		entry, _, err := cursor.workspace.read(ctx, key)
		return key, entry, true, err
	}
	if len(cursor.rows) == 0 {
		if err := cursor.refill(ctx); err != nil {
			return "", structuralDirectoryWork{}, false, err
		}
	}
	if len(cursor.rows) == 0 {
		return "", structuralDirectoryWork{}, false, nil
	}
	row := cursor.rows[0]
	cursor.rows = cursor.rows[1:]
	return row.key, row.entry, true, nil
}
func (cursor *directoryWorkCursor) refill(ctx context.Context) error {
	order, compare := "ASC", ">"
	if cursor.reverse {
		order, compare = "DESC", "<"
	}
	query := "SELECT path,override,value,flags,previous FROM directories WHERE (flags & ?) != 0"
	arguments := []any{cursor.mask}
	if cursor.mask == 0 {
		query = "SELECT path,override,value,flags,previous FROM directories WHERE 1=1"
		arguments = nil
	}
	if cursor.started {
		query += " AND path " + compare + " ?"
		arguments = append(arguments, []byte(cursor.after))
	}
	query += fmt.Sprintf(" ORDER BY path %s LIMIT %d", order, indexBatchSize)
	if cursor.flagsOnly {
		query = strings.Replace(query, "path,override,value,flags,previous", "path,flags", 1)
	}
	statement, err := cursor.workspace.statement(ctx, query) //nolint:sqlclosecheck // Workspace.Close releases this reusable statement.
	if err != nil {
		return err
	}
	rows, err := statement.QueryContext(ctx, arguments...)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var key, value, previous []byte
		var entry structuralDirectoryWork
		if cursor.flagsOnly {
			if err := rows.Scan(&key, &entry.flags); err != nil {
				return err
			}
			cursor.rows = append(cursor.rows, directoryWorkRow{key: string(key), entry: entry})
			continue
		}
		if err := rows.Scan(&key, &entry.override, &value, &entry.flags, &previous); err != nil {
			return err
		}
		if len(value) > 0 {
			entry.value, err = decodeDirectoryWorkRecord(string(key), value)
			if err != nil {
				return err
			}
		}
		if len(previous) > 0 {
			entry.previous, err = decodeDirectoryWorkRecord(string(key), previous)
			if err != nil {
				return err
			}
			entry.hasPrevious = true
		}
		cursor.workspace.cacheWork(string(key), entry, true)
		cursor.rows = append(cursor.rows, directoryWorkRow{key: string(key), entry: entry})
	}
	if len(cursor.rows) > 0 {
		cursor.after = cursor.rows[len(cursor.rows)-1].key
	}
	cursor.started = true
	return rows.Err()
}

type directoryBaseCursor struct {
	index structuralDirectoryIndex
	after string
	items []pagedview.RangeItem[structuralDirectory]
}

func (cursor *directoryBaseCursor) next(ctx context.Context) (structuralDirectory, bool, error) {
	if len(cursor.items) == 0 {
		tree := pagedview.RangeIndex[structuralDirectory]{Root: cursor.index.root, Store: cursor.index.store}
		var err error
		cursor.items, err = tree.ReadAfter(ctx, cursor.after, indexBatchSize)
		if err != nil {
			return structuralDirectory{}, false, err
		}
		if len(cursor.items) == 0 {
			return structuralDirectory{}, false, nil
		}
		cursor.after = cursor.items[len(cursor.items)-1].Key
	}
	item := cursor.items[0]
	cursor.items = cursor.items[1:]
	return item.Value, true, nil
}

func (w *structuralDirectoryWorkspace) Visit(ctx context.Context, visit func(structuralDirectory) error) error {
	return w.visit(ctx, "", visit)
}
func (w *structuralDirectoryWorkspace) visit(ctx context.Context, prefix string, visit func(structuralDirectory) error) error {
	base := directoryBaseCursor{index: w.base, after: prefix}
	delta := directoryWorkCursor{workspace: w}
	if prefix != "" && w.tx != nil {
		delta.started = true
		delta.after = prefix
	}
	current, hasBase, err := base.next(ctx)
	if err != nil {
		return err
	}
	key, entry, hasDelta, err := delta.next(ctx)
	if err != nil {
		return err
	}
	for hasBase || hasDelta {
		if err := ctx.Err(); err != nil {
			return err
		}
		var value structuralDirectory
		var present bool
		advanceBase, advanceDelta := false, false
		switch {
		case !hasDelta || hasBase && current.observation.Path < key:
			value, present, advanceBase = current, true, true
		case !hasBase || key < current.observation.Path:
			value, present, advanceDelta = entry.value, entry.override == 1, true
		default:
			value, present = current, entry.override != 2
			if entry.override == 1 {
				value = entry.value
			}
			advanceBase, advanceDelta = true, true
		}
		if present {
			name := value.observation.Path
			if prefix != "" && name < prefix {
				present = false
			}
			if present && prefix != "" && !strings.HasPrefix(name, prefix) {
				return nil
			}
			if present {
				if err := visit(value); err != nil {
					return err
				}
			}
		}
		if advanceBase {
			current, hasBase, err = base.next(ctx)
			if err != nil {
				return err
			}
		}
		if advanceDelta {
			key, entry, hasDelta, err = delta.next(ctx)
			if err != nil {
				return err
			}
		}
	}
	return nil
}
func (w *structuralDirectoryWorkspace) DeleteSubtree(ctx context.Context, dir string) error {
	if dir == "." {
		return w.Visit(ctx, func(value structuralDirectory) error { return w.delete(ctx, value.observation.Path) })
	}
	if err := w.delete(ctx, dir); err != nil {
		return err
	}
	return w.visit(ctx, dir+"/", func(value structuralDirectory) error { return w.delete(ctx, value.observation.Path) })
}
func (w *structuralDirectoryWorkspace) freeze(ctx context.Context, pages pagedview.PageStore[structuralDirectory]) (structuralDirectoryIndex, error) {
	if w.cachedValid && w.cachedRevision == w.revisions {
		return w.cached, nil
	}
	tree := pagedview.RangeIndex[structuralDirectory]{Store: pages, Root: w.base.root}
	if w.base.root == 0 || w.tx != nil {
		if err := tree.BuildSorted(ctx, func(yield func(pagedview.RangeItem[structuralDirectory]) error) error {
			return w.Visit(ctx, func(value structuralDirectory) error {
				return yield(pagedview.RangeItem[structuralDirectory]{Key: value.observation.Path, Value: value, Weight: 1})
			})
		}); err != nil {
			return structuralDirectoryIndex{}, err
		}
	} else {
		updates := make([]pagedview.RangeItem[structuralDirectory], 0, indexBatchSize)
		flush := func() error { err := tree.SetBatch(ctx, updates); updates = updates[:0]; return err }
		err := w.visitWork(ctx, 0, false, func(key string, entry structuralDirectoryWork) error {
			if entry.override == 2 {
				return tree.Remove(ctx, key)
			}
			if entry.override == 1 {
				updates = append(updates, pagedview.RangeItem[structuralDirectory]{Key: key, Value: entry.value, Weight: 1})
			}
			if len(updates) == indexBatchSize {
				return flush()
			}
			return nil
		})
		if err != nil {
			return structuralDirectoryIndex{}, err
		}
		if err := flush(); err != nil {
			return structuralDirectoryIndex{}, err
		}
	}
	w.cached = structuralDirectoryIndex{root: tree.Root, count: w.count, store: pages}
	w.cachedRevision = w.revisions
	w.cachedValid = true
	return w.cached, nil
}
