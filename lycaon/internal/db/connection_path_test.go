package db

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestStorePathTreatsURICharactersAsFilenameBytes(t *testing.T) {
	names := []string{"hash#fragment.db", "percent%23.db", "space and café.db"}
	if runtime.GOOS != "windows" {
		names = append(names, "query?mode=memory.db")
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), name)
			store, err := Open(path)
			testutil.FailErr(t, "create literal path", err)
			_, err = store.ExecContext(t.Context(), "CREATE TABLE path_probe (value TEXT NOT NULL)")
			testutil.FailErr(t, "create path probe", err)
			_, err = store.ExecContext(t.Context(), "INSERT INTO path_probe VALUES ('retained')")
			testutil.FailErr(t, "write path probe", err)
			testutil.FailErr(t, "close writer", store.Close())
			info, err := os.Stat(path)
			testutil.FailErr(t, "stat literal path", err)
			if info.Size() == 0 {
				t.Fatal("database was not written to the requested path")
			}
			reader, err := OpenReadOnly(t.Context(), path)
			testutil.FailErr(t, "reopen literal path", err)
			t.Cleanup(func() { testutil.FailErr(t, "close reader", reader.Close()) })
			var value string
			testutil.FailErr(t, "read persisted value", reader.QueryRowContext(t.Context(), "SELECT value FROM path_probe").Scan(&value))
			if value != "retained" {
				t.Fatalf("read a different database: %q", value)
			}
			if _, err := reader.ExecContext(t.Context(), "DELETE FROM path_probe"); err == nil {
				t.Fatal("filename changed the read-only connection policy")
			}
		})
	}
}
