package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func openDBWithSchemaSQL(dbPath string, schemaSQL string) (*sql.DB, error) {
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create db directory: %w", err)
	}
	ctx := context.Background()
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(on)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)", dbPath)
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	if _, err := sqlDB.ExecContext(ctx, schemaSQL); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("exec schema: %w", err)
	}
	sqlDB.SetMaxOpenConns(writerOpenConns)
	return sqlDB, nil
}

func TestOpenSchemaMatchesEmbeddedSchemaSQL(t *testing.T) {
	ctx := t.Context()
	schema, err := schemaFS.ReadFile("schema.sql")
	testutil.FailErr(t, "read embedded schema.sql", err)

	embeddedPath := filepath.Join(t.TempDir(), "embedded.db")
	embeddedDB, err := openDBWithSchemaSQL(embeddedPath, string(schema))
	testutil.FailErr(t, "apply schema.sql", err)
	defer embeddedDB.Close()

	openPath := filepath.Join(t.TempDir(), "open.db")
	openDB, err := Open(openPath)
	testutil.FailErr(t, "Open", err)
	defer openDB.Close()

	want, err := ReadShape(ctx, embeddedDB)
	testutil.FailErr(t, "embedded shape", err)
	got, err := ReadShape(ctx, openDB)
	testutil.FailErr(t, "Open shape", err)
	if diff := want.Diff(got); len(diff) > 0 {
		t.Fatalf("Open() must apply embedded schema.sql with no extra DDL:\n  %s", strings.Join(diff, "\n  "))
	}
}

func TestOpenSurfacesSchemaExecError(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "broken.db")
	_, err := openDBWithSchemaSQL(dbPath, "CREATE TABLE bad (id); INVALID SQL STATEMENT;")
	if err == nil {
		t.Fatal("expected schema exec error")
	}
	if !strings.Contains(err.Error(), "exec schema") {
		t.Fatalf("error = %q want exec schema wrapper", err)
	}
}

func TestProjectRootLabelSchemaInvariant(t *testing.T) {
	sqlDB, err := Open(filepath.Join(t.TempDir(), "labels.db"))
	testutil.FailErr(t, "open store", err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	_, err = sqlDB.ExecContext(t.Context(), `
		INSERT INTO projects (id, last_opened_at, created_at)
		VALUES ('project', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')
	`)
	testutil.FailErr(t, "insert project", err)

	invalid := []any{nil, "", " padded ", "@root", "a/b", `a\b`, "a\tb", strings.Repeat("x", 65)}
	for index, label := range invalid {
		_, err = sqlDB.ExecContext(t.Context(), `
			INSERT INTO project_roots (id, project_id, path, label, added_at)
			VALUES (?, 'project', ?, ?, '2026-01-01T00:00:00Z')
		`, fmt.Sprintf("root-%d", index), fmt.Sprintf("/tmp/root-%d", index), label)
		if err == nil {
			t.Fatalf("project root label %#v was accepted", label)
		}
	}
}
