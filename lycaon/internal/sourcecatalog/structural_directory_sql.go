package sourcecatalog

import (
	"context"
	"database/sql"

	"github.com/lycaon/lycaon/internal/pagedview"
)

type cachedDirectoryWork struct {
	entry structuralDirectoryWork
	found bool
}

func (w *structuralDirectoryWorkspace) statement(ctx context.Context, query string) (*sql.Stmt, error) {
	if statement := w.statements[query]; statement != nil {
		return statement, nil
	}
	statement, err := w.tx.PrepareContext(ctx, query)
	if err != nil {
		return nil, err
	}
	w.statements[query] = statement
	return statement, nil
}
func (w *structuralDirectoryWorkspace) cacheWork(key string, entry structuralDirectoryWork, found bool) {
	if w.hot == nil {
		w.hot = pagedview.NewCache[string, cachedDirectoryWork](8192, 8<<20)
	}
	w.hot.Put(key, cachedDirectoryWork{entry: entry, found: found}, directoryWorkBytes(key, entry))
}
func (w *structuralDirectoryWorkspace) cachedWork(key string) (structuralDirectoryWork, bool, bool) {
	if w.hot == nil {
		return structuralDirectoryWork{}, false, false
	}
	cached, known := w.hot.Get(key)
	return cached.entry, cached.found, known
}

// Finalization consumes its work in one SQL pass, without decoding directory records.
func (w *structuralDirectoryWorkspace) ClearFlags(ctx context.Context, mask uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if w.tx == nil {
		for key, entry := range w.memory {
			entry.flags &^= mask
			w.memory[key] = entry
		}
		return nil
	}
	statement, err := w.statement(ctx, "UPDATE directories SET flags=flags & ~? WHERE (flags & ?) != 0") //nolint:sqlclosecheck // Workspace.Close releases this reusable statement.
	if err != nil {
		return err
	}
	_, err = statement.ExecContext(ctx, mask, mask)
	if err == nil {
		w.hot = nil
	}
	return err
}
