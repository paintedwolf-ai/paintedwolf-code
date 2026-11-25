// Package testdbfixture centralizes disposable SQL store construction for tests.
package testdbfixture

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/hostlock"
	"github.com/lycaon/lycaon/internal/testutil"
)

var (
	templateOnce  sync.Once
	templateImage []byte
	templateErr   error
)

// migratedImage builds one migrated database per test process and keeps only its bytes,
// so no file outlives the process.
func migratedImage() ([]byte, error) {
	templateOnce.Do(func() {
		dir, err := os.MkdirTemp("", "lycaon-testdb-template-*")
		if err != nil {
			templateErr = err
			return
		}
		defer func() { _ = os.RemoveAll(dir) }()
		path := filepath.Join(dir, "template.db")
		store, err := db.OpenWithOptions(context.Background(), path, db.UpgradeHooks{}, db.StoreOptions{FastTestSync: true})
		if err != nil {
			templateErr = err
			return
		}
		if err := store.Close(); err != nil {
			templateErr = err
			return
		}
		templateImage, templateErr = os.ReadFile(path)
	})
	return templateImage, templateErr
}

func writeImage(image []byte, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	return os.WriteFile(dst, image, 0o600)
}

// Open creates a database below t.TempDir and registers its close operation.
func Open(t testing.TB, name string) *db.Store {
	t.Helper()
	return OpenPath(t, filepath.Join(t.TempDir(), name))
}

// OpenPath opens a disposable database at path and registers cleanup.
func OpenPath(t testing.TB, path string) *db.Store {
	t.Helper()
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if image, imageErr := migratedImage(); imageErr == nil {
			_ = writeImage(image, path)
		}
	}
	store, err := db.OpenWithOptions(context.Background(), path, db.UpgradeHooks{}, db.StoreOptions{FastTestSync: true})
	testutil.FailErr(t, "open test database", err)
	t.Cleanup(func() {
		testutil.FailErr(t, "close test database", store.Close())
	})
	return store
}

// ClaimStore takes and binds the engine claim for a store opened at path.
func ClaimStore(t testing.TB, path string) *hostlock.Claim {
	t.Helper()
	claim, err := hostlock.AcquireStore(path)
	testutil.FailErr(t, "acquire store claim", err)
	t.Cleanup(claim.Release)
	testutil.FailErr(t, "bind store claim", claim.BindStore())
	return claim
}
