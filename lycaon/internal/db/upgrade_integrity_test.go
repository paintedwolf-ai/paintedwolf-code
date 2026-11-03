package db

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestUpgradeStructureRestoresOnlyItsHeldConnection(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		name := "healthy"
		if invalid {
			name = "foreign key violation"
		}
		t.Run(name, func(t *testing.T) {
			database, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "store.db"))
			testutil.FailErr(t, "open upgrade integrity fixture", err)
			defer func() { _ = database.Close() }()
			database.SetMaxOpenConns(2)
			database.SetMaxIdleConns(2)
			other, err := database.Conn(t.Context())
			testutil.FailErr(t, "hold unrelated reader", err)
			defer func() { _ = other.Close() }()
			_, err = other.ExecContext(t.Context(), "PRAGMA cache_size=91")
			testutil.FailErr(t, "set unrelated cache", err)
			work, err := database.Conn(t.Context())
			testutil.FailErr(t, "hold upgrade reader", err)
			_, err = work.ExecContext(t.Context(), `PRAGMA cache_size=-2000;
 CREATE TABLE parent(id INTEGER PRIMARY KEY);
 CREATE TABLE child(parent_id INTEGER REFERENCES parent(id));
 INSERT INTO parent VALUES(1); INSERT INTO child VALUES(1)`)
			testutil.FailErr(t, "seed upgrade references", err)
			if invalid {
				_, err = work.ExecContext(t.Context(), "INSERT INTO child VALUES(2)")
				testutil.FailErr(t, "seed rejected foreign key", err)
			}
			testutil.FailErr(t, "release upgrade reader", work.Close())
			err = validateUpgradeStructure(t.Context(), database)
			if invalid {
				var failure *StoreIncompatibleError
				if !errors.As(err, &failure) || failure.Reason != RecoveryReasonIntegrityFailed {
					t.Fatalf("foreign key failure lost integrity classification: %v", err)
				}
			} else {
				testutil.FailErr(t, "validate healthy upgrade", err)
			}
			work, err = database.Conn(t.Context())
			testutil.FailErr(t, "reacquire restored reader", err)
			defer func() { _ = work.Close() }()
			assertUpgradeIntegrityCache(t, work, -2000)
			assertUpgradeIntegrityCache(t, other, 91)
		})
	}
}

func TestUpgradeStructurePreservesConnectionCancellation(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	testutil.FailErr(t, "open canceled upgrade fixture", err)
	defer func() { _ = database.Close() }()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := validateUpgradeStructure(ctx, database); !errors.Is(err, context.Canceled) {
		t.Fatalf("upgrade reader cancellation=%v", err)
	}
}

func assertUpgradeIntegrityCache(t *testing.T, connection *sql.Conn, want int64) {
	t.Helper()
	var got int64
	err := connection.QueryRowContext(t.Context(), "PRAGMA cache_size").Scan(&got)
	testutil.FailErr(t, "read restored upgrade cache", err)
	if got != want {
		t.Fatalf("cache_size=%d, want %d", got, want)
	}
}
