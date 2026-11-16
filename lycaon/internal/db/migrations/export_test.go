package migrations

import (
	"context"
	"database/sql"
)

func writeLayoutChunk(ctx context.Context, tx *sql.Tx, insert *sql.Stmt, entries []layoutEntry) (layoutChunk, error) {
	return insertLayoutChunk(ctx, tx, insert, buildLayoutChunk(entries), entries)
}
