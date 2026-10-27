package scantest

import (
	"context"
	"database/sql"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
)

// idleWindow must outlast any polling loop the quiet assertion rules out.
const idleWindow = 1100 * time.Millisecond

// CountedDB counts storage reads and write transactions.
type CountedDB struct {
	db.Handle
	Reads  atomic.Int64
	Writes atomic.Int64
}

func (d *CountedDB) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	d.Reads.Add(1)
	return d.Handle.QueryContext(ctx, query, args...)
}

func (d *CountedDB) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	d.Reads.Add(1)
	return d.Handle.QueryRowContext(ctx, query, args...)
}

func (d *CountedDB) BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error) {
	d.Writes.Add(1)
	return d.Handle.BeginTx(ctx, opts)
}

// RunService runs a scan service until the test ends and requires a clean stop.
func RunService(t *testing.T, run func(context.Context) error) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Errorf("scan service stopped: %v", err)
		}
	})
}

// AssertQuiet fails when an idle scan service touches storage.
func AssertQuiet(t *testing.T, database *CountedDB) {
	t.Helper()
	// Startup notifications drain before the idle window opens.
	time.Sleep(100 * time.Millisecond)
	reads, writes := database.Reads.Load(), database.Writes.Load()
	time.Sleep(idleWindow)
	if database.Reads.Load() != reads || database.Writes.Load() != writes {
		t.Fatalf("idle scan service touched storage: reads %d -> %d, writes %d -> %d",
			reads, database.Reads.Load(), writes, database.Writes.Load())
	}
}
