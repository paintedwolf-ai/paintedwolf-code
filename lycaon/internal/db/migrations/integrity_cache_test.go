package migrations

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	_ "modernc.org/sqlite"
)

func TestIntegrityCacheRestoresAfterSuccessFailureAndCancellation(t *testing.T) {
	for _, outcome := range []string{"success", "failure", "cancellation"} {
		t.Run(outcome, func(t *testing.T) {
			connection := integrityCacheConnection(t)
			_, err := connection.ExecContext(t.Context(), "PRAGMA cache_size=37")
			testutil.FailErr(t, "set original page cache", err)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			failure := errors.New("validation failed")
			err = WithIntegrityCache(ctx, connection, func() error {
				assertIntegrityCache(t, connection, -65536)
				switch outcome {
				case "failure":
					return failure
				case "cancellation":
					cancel()
					return ctx.Err()
				default:
					return nil
				}
			})
			switch outcome {
			case "success":
				testutil.FailErr(t, "validate with scoped cache", err)
			case "failure":
				if !errors.Is(err, failure) {
					t.Fatalf("validation failure was lost: %v", err)
				}
			case "cancellation":
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("validation cancellation was lost: %v", err)
				}
			}
			assertIntegrityCache(t, connection, 37)
		})
	}
}

func TestIntegrityCachePreservesTransactionAndNegativeSetting(t *testing.T) {
	connection := integrityCacheConnection(t)
	_, err := connection.ExecContext(t.Context(), "PRAGMA cache_size=-2000; CREATE TABLE retained(value TEXT)")
	testutil.FailErr(t, "seed transaction fixture", err)
	tx, err := connection.BeginTx(t.Context(), nil)
	testutil.FailErr(t, "begin retained transaction", err)
	defer func() { _ = tx.Rollback() }()
	err = WithIntegrityCache(t.Context(), connection, func() error {
		assertIntegrityCache(t, tx, -65536)
		_, err := tx.ExecContext(t.Context(), "INSERT INTO retained VALUES('committed')")
		return err
	})
	testutil.FailErr(t, "validate inside transaction", err)
	assertIntegrityCache(t, tx, -2000)
	testutil.FailErr(t, "commit retained transaction", tx.Commit())
	var value string
	err = connection.QueryRowContext(t.Context(), "SELECT value FROM retained").Scan(&value)
	testutil.FailErr(t, "read retained transaction", err)
	if value != "committed" {
		t.Fatalf("transaction value=%q", value)
	}
}

func TestIntegrityCachePreservesConfigurationAndRestorationFailures(t *testing.T) {
	for _, setting := range []string{"PRAGMA cache_size=-65536", "PRAGMA cache_size=37"} {
		t.Run(setting, func(t *testing.T) {
			connection := integrityCacheConnection(t)
			_, err := connection.ExecContext(t.Context(), "PRAGMA cache_size=37")
			testutil.FailErr(t, "seed original cache setting", err)
			failure := errors.New("cache configuration failed")
			validationFailure := errors.New("integrity failed")
			store := &integrityCacheFailure{DBTX: connection, statement: setting, failure: failure}
			called := false
			err = WithIntegrityCache(t.Context(), store, func() error {
				called = true
				return validationFailure
			})
			if !errors.Is(err, failure) {
				t.Fatalf("cache error was lost: %v", err)
			}
			if setting == "PRAGMA cache_size=-65536" {
				if called {
					t.Fatal("validation ran without its configured cache")
				}
				assertIntegrityCache(t, connection, 37)
			} else if !called || !errors.Is(err, validationFailure) {
				t.Fatalf("restoration hid the validation failure: called=%v err=%v", called, err)
			}
		})
	}
}

func TestIntegrityCacheDoesNotStartWithCanceledContext(t *testing.T) {
	connection := integrityCacheConnection(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := WithIntegrityCache(ctx, connection, func() error {
		t.Fatal("validation started with canceled cache inspection")
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cache inspection cancellation=%v", err)
	}
}

func integrityCacheConnection(t *testing.T) *sql.Conn {
	t.Helper()
	database, err := sql.Open("sqlite", ":memory:")
	testutil.FailErr(t, "open integrity cache fixture", err)
	t.Cleanup(func() { _ = database.Close() })
	connection, err := database.Conn(t.Context())
	testutil.FailErr(t, "hold integrity cache connection", err)
	t.Cleanup(func() { _ = connection.Close() })
	return connection
}

func assertIntegrityCache(t *testing.T, database DBTX, want int64) {
	t.Helper()
	var got int64
	err := database.QueryRowContext(t.Context(), "PRAGMA cache_size").Scan(&got)
	testutil.FailErr(t, "read scoped cache setting", err)
	if got != want {
		t.Fatalf("cache_size=%d, want %d", got, want)
	}
}

type integrityCacheFailure struct {
	DBTX
	statement string
	failure   error
}

func (s *integrityCacheFailure) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if query == s.statement {
		return nil, s.failure
	}
	return s.DBTX.ExecContext(ctx, query, args...)
}
