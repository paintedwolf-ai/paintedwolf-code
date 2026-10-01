package db

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

// A quit during active work spends the whole ordered drain budget before the
// store closes. An exhausted context must not skip the clean marker and the WAL
// truncate, or every launch after a busy quit pays quick_check on the startup
// path and a whole-store audit behind it.
func TestShutdownMarksCleanWithAnAlreadySpentContext(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "store.db")
	store, err := Open(path)
	testutil.FailErr(t, "open store", err)

	ctx := context.Background()
	_, err = store.ExecContext(ctx, `INSERT INTO store_meta(key, value) VALUES('shutdown_probe','1')
ON CONFLICT(key) DO UPDATE SET value = excluded.value`)
	testutil.FailErr(t, "write store", err)

	spent, cancel := context.WithTimeout(ctx, time.Nanosecond)
	defer cancel()
	<-spent.Done()
	testutil.FailErr(t, "shutdown store", store.Shutdown(spent))

	if info, statErr := os.Stat(path + "-wal"); statErr == nil && info.Size() > 0 {
		t.Fatalf("wal was not truncated at shutdown: %d bytes", info.Size())
	}

	reopened, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	testutil.FailErr(t, "reopen store", err)
	defer func() { _ = reopened.Close() }()
	clean, err := lastShutdownWasClean(ctx, reopened)
	testutil.FailErr(t, "read shutdown state", err)
	if !clean {
		t.Fatal("shutdown did not record a clean close, so the next boot pays a full integrity check")
	}
}

// A caller with room to spare keeps its own deadline: the floor is a floor, not
// an override that hides a caller's cancellation.
func TestShutdownKeepsAGenerousCallerDeadline(t *testing.T) {
	parent, cancel := context.WithTimeout(context.Background(), StoreShutdownFloor*4)
	defer cancel()
	ctx, release := shutdownContext(parent)
	defer release()

	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("shutdown context lost the caller deadline")
	}
	if time.Until(deadline) <= StoreShutdownFloor {
		t.Fatalf("shutdown context shortened a generous deadline to %v", time.Until(deadline))
	}
}
