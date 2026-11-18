package laws

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite" // the repository's SQLite driver
)

// SchemaPath is the complete fresh schema of the host store.
const SchemaPath = "lycaon/internal/db/schema.sql"

// Schema returns SchemaPath executed in an in-memory SQLite database, once
// per corpus. Rules read the live schema through SQLite's catalog:
// sqlite_schema and the table pragmas. A schema that does not execute ends
// the check.
func (c *Corpus) Schema() *sql.DB {
	db, err := c.schema.get(func() (*sql.DB, error) {
		text := c.Text(SchemaPath)
		db, err := sql.Open("sqlite", "file:laws?mode=memory&cache=private")
		if err != nil {
			return nil, err
		}
		// Each connection to a private in-memory database is its own database.
		db.SetMaxOpenConns(1)
		if _, err := db.Exec(text); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("execute %s: %w", SchemaPath, err)
		}
		return db, nil
	})
	if err != nil {
		panic(abort{fmt.Errorf("open the live schema: %w", err)})
	}
	return db
}
