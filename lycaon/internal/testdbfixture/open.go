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
	templateOnce sync.Once
	templateFile string
	templateErr  error
)

func ensureTemplate() (string, error) {
	templateOnce.Do(func() {
		dir, err := os.MkdirTemp("", "lycaon-testdb-template-*")
		if err != nil {
			templateErr = err
			return
		}
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
		templateFile = path
	})
	return templateFile, templateErr
}

// Open creates a database below t.TempDir and registers its close operation.
func Open(t testing.TB, name string) *db.Store {
	t.Helper()
	return OpenPath(t, filepath.Join(t.TempDir(), name))
}

// OpenPath opens a disposable database at path and registers cleanup.
func OpenPath(t testing.TB, path string) *db.Store {
	t.Helper()
	opts := db.StoreOptions{FastTestSync: true}
	var store *db.Store
	var err error
	if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
		tmpl, tmplErr := ensureTemplate()
		testutil.FailErr(t, "prepare test database template", tmplErr)
		store, err = db.OpenCopy(context.Background(), tmpl, path, opts)
	} else {
		store, err = db.OpenWithOptions(context.Background(), path, db.UpgradeHooks{}, opts)
	}
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
