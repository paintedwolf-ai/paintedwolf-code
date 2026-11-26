package testdbfixture

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestOpenPathBuildsMigratedStoresWithoutLeavingProcessTemp(t *testing.T) {
	stores := t.TempDir()
	temp := t.TempDir()
	t.Setenv("TMPDIR", temp)
	templateOnce, templateImage, templateErr = sync.Once{}, nil, nil

	for _, name := range []string{"first.db", "second.db"} {
		store := OpenPath(t, filepath.Join(stores, name))
		var version int
		testutil.FailErr(t, "read schema version",
			store.QueryRowContext(t.Context(), "PRAGMA user_version").Scan(&version))
		if version != db.SchemaVersion {
			t.Fatalf("%s schema version = %d, want %d", name, version, db.SchemaVersion)
		}
	}

	entries, err := os.ReadDir(temp)
	testutil.FailErr(t, "read process temp", err)
	for _, entry := range entries {
		t.Errorf("process temp kept %s", entry.Name())
	}
}
