package wiring

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/testutil"
)

var (
	databaseBaselineOnce sync.Once
	databaseBaselineData []byte
	databaseBaselineErr  error
)

// cloneDatabaseBaseline reuses one current-schema store per test process.
func cloneDatabaseBaseline(t testing.TB, path string) {
	t.Helper()
	databaseBaselineOnce.Do(buildDatabaseBaseline)
	testutil.FailErr(t, "build database baseline", databaseBaselineErr)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		testutil.FailErr(t, "create baseline destination", err)
	}
	_, err := fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.PathLocation(path),
		Source:   bytes.NewReader(databaseBaselineData),
		Mode:     0o600,
		DirMode:  0o700,
	})
	testutil.FailErr(t, "clone database baseline", err)
}

func buildDatabaseBaseline() {
	dir, err := os.MkdirTemp("", "paintedwolf-test-db-baseline-")
	if err != nil {
		databaseBaselineErr = err
		return
	}
	defer os.RemoveAll(dir)

	path := filepath.Join(dir, "baseline.db")
	sqlDB, err := db.Open(path)
	if err != nil {
		databaseBaselineErr = err
		return
	}
	if _, err = db.CheckpointWAL(context.Background(), sqlDB); err == nil {
		err = sqlDB.Close()
	} else {
		_ = sqlDB.Close()
	}
	if err != nil {
		databaseBaselineErr = err
		return
	}
	databaseBaselineData, databaseBaselineErr = os.ReadFile(path)
}
