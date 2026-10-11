package app

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestIntegrityFailureStopsRunnerAndStoreCoupledMaintenance(t *testing.T) {
	store, err := db.Open(filepath.Join(t.TempDir(), "store.db"))
	testutil.FailErr(t, "open store", err)
	t.Cleanup(func() { _ = store.Close() })
	_, err = store.ExecContext(t.Context(), `PRAGMA foreign_keys=OFF`)
	testutil.FailErr(t, "disable fixture foreign keys", err)
	_, err = store.ExecContext(t.Context(), `INSERT INTO project_roots(id,project_id,path,label,added_at) VALUES('invalid','absent','/absent','absent','2026-01-01T00:00:00Z')`)
	testutil.FailErr(t, "inject damaged relation", err)
	app := &ServeApp{resources: &runtimeResources{db: store}}
	runner := &backgroundRunner{name: "store-integrity-audit", run: func(ctx context.Context) error { return db.RunIntegrityAudit(ctx, store) }, retryDelay: func(int) time.Duration {
		t.Error("permanent integrity failure entered retry")
		return time.Millisecond
	}}
	done := make(chan struct{})
	go func() { defer close(done); app.superviseRunner(t.Context(), runner) }()
	select {
	case <-app.storeFailed():
	case <-time.After(5 * time.Second):
		t.Fatal("serve loop did not receive the integrity failure")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("integrity runner did not settle")
	}
	if err := (storeAccessGuard{store: store}).Verify(); !errors.Is(err, db.ErrStoreIncompatible) {
		t.Fatalf("maintenance guard = %v", err)
	}
	if err := (delegationWiring{&serveBuilder{db: store}}).reconcileStoreCoupledStorage(t.Context()); !errors.Is(err, db.ErrStoreIncompatible) {
		t.Fatalf("reconcile = %v", err)
	}
}
