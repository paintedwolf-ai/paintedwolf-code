package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestWALReaderContinuesWhileWriterTransactionIsOpen(t *testing.T) {
	sqlDB, err := Open(filepath.Join(t.TempDir(), "concurrency.db"))
	testutil.FailErr(t, "open", err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	_, err = sqlDB.ExecContext(t.Context(), `INSERT INTO store_meta(key, value) VALUES('visible', 'before')`)
	testutil.FailErr(t, "seed", err)

	tx, err := sqlDB.BeginTx(t.Context(), nil)
	testutil.FailErr(t, "begin writer", err)
	t.Cleanup(func() { _ = tx.Rollback() })
	_, err = tx.ExecContext(t.Context(), `UPDATE store_meta SET value = 'after' WHERE key = 'visible'`)
	testutil.FailErr(t, "write uncommitted value", err)

	read := make(chan string, 1)
	errs := make(chan error, 1)
	go func() {
		var value string
		if queryErr := sqlDB.QueryRowContext(t.Context(),
			`SELECT value FROM store_meta WHERE key = 'visible'`).Scan(&value); queryErr != nil {
			errs <- queryErr
			return
		}
		read <- value
	}()
	select {
	case queryErr := <-errs:
		t.Fatalf("read during writer transaction: %v", queryErr)
	case value := <-read:
		if value != "before" {
			t.Fatalf("reader saw %q, want committed snapshot before", value)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reader blocked behind writer transaction; WAL read concurrency is not active")
	}
	testutil.FailErr(t, "commit writer", tx.Commit())
}

func TestReadTransactionKeepsAConsistentSnapshot(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "read-snapshot.db"))
	testutil.FailErr(t, "open", err)
	t.Cleanup(func() { _ = store.Close() })
	_, err = store.ExecContext(t.Context(), `INSERT INTO store_meta(key, value) VALUES('visible', 'before')`)
	testutil.FailErr(t, "seed", err)

	tx, err := store.BeginReadTx(t.Context())
	testutil.FailErr(t, "begin read", err)
	t.Cleanup(func() { _ = tx.Rollback() })
	var value string
	testutil.FailErr(t, "establish snapshot",
		tx.QueryRowContext(t.Context(), `SELECT value FROM store_meta WHERE key = 'visible'`).Scan(&value))

	_, err = store.ExecContext(t.Context(), `UPDATE store_meta SET value = 'after' WHERE key = 'visible'`)
	testutil.FailErr(t, "update through writer", err)
	testutil.FailErr(t, "read snapshot",
		tx.QueryRowContext(t.Context(), `SELECT value FROM store_meta WHERE key = 'visible'`).Scan(&value))
	if value != "before" {
		t.Fatalf("read transaction saw %q, want before", value)
	}
	testutil.FailErr(t, "read current",
		store.QueryRowContext(t.Context(), `SELECT value FROM store_meta WHERE key = 'visible'`).Scan(&value))
	if value != "after" {
		t.Fatalf("current reader saw %q, want after", value)
	}
}

func TestInterruptedReaderLeavesPoolUsable(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "interrupted-reader.db"))
	testutil.FailErr(t, "open", err)
	t.Cleanup(func() { _ = store.Close() })
	_, err = store.ExecContext(t.Context(), `INSERT INTO store_meta(key, value) VALUES('counter', '0')`)
	testutil.FailErr(t, "seed", err)
	store.reader.SetMaxOpenConns(1)
	store.reader.SetMaxIdleConns(1)
	conn, err := store.reader.Conn(t.Context())
	testutil.FailErr(t, "acquire reader", err)

	queryCtx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	var sum int64
	err = conn.QueryRowContext(queryCtx, `
		WITH RECURSIVE count(value) AS (
			SELECT CAST(value AS INTEGER) FROM store_meta WHERE key = 'counter'
			UNION ALL
			SELECT value + 1 FROM count WHERE value < 100000000
		)
		SELECT sum(value) FROM count
	`).Scan(&sum)
	if err == nil {
		t.Fatal("reader query completed before cancellation")
	}
	if !errors.Is(err, context.DeadlineExceeded) && !strings.Contains(err.Error(), "interrupted") {
		t.Fatalf("interrupted reader error = %v", err)
	}
	testutil.FailErr(t, "release interrupted reader", conn.Close())

	var value string
	testutil.FailErr(t, "read after interrupted connection",
		store.QueryRowContext(t.Context(), `SELECT value FROM store_meta WHERE key = 'counter'`).Scan(&value))
	if value != "0" {
		t.Fatalf("reader value after interruption = %q, want 0", value)
	}
}

func TestEveryPooledConnectionEnforcesPragmas(t *testing.T) {
	sqlDB, err := Open(filepath.Join(t.TempDir(), "pool.db"))
	testutil.FailErr(t, "open", err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	connections := make([]*sql.Conn, 0, readerOpenConns)
	for range readerOpenConns {
		conn, err := sqlDB.reader.Conn(t.Context())
		testutil.FailErr(t, "acquire pooled connection", err)
		connections = append(connections, conn)
	}
	t.Cleanup(func() {
		for _, conn := range connections {
			_ = conn.Close()
		}
	})

	for i, conn := range connections {
		var foreignKeys, busyTimeout, queryOnly int
		var journalMode string
		testutil.FailErr(t, fmt.Sprintf("connection %d foreign_keys", i),
			conn.QueryRowContext(t.Context(), `PRAGMA foreign_keys`).Scan(&foreignKeys))
		testutil.FailErr(t, fmt.Sprintf("connection %d busy_timeout", i),
			conn.QueryRowContext(t.Context(), `PRAGMA busy_timeout`).Scan(&busyTimeout))
		testutil.FailErr(t, fmt.Sprintf("connection %d journal_mode", i),
			conn.QueryRowContext(t.Context(), `PRAGMA journal_mode`).Scan(&journalMode))
		testutil.FailErr(t, fmt.Sprintf("connection %d query_only", i),
			conn.QueryRowContext(t.Context(), `PRAGMA query_only`).Scan(&queryOnly))
		if foreignKeys != 1 || busyTimeout != 10_000 || journalMode != "wal" || queryOnly != 1 {
			t.Fatalf("connection %d pragmas = foreign_keys:%d busy_timeout:%d journal_mode:%s query_only:%d",
				i, foreignKeys, busyTimeout, journalMode, queryOnly)
		}
	}
	var writerForeignKeys, writerBusyTimeout int
	testutil.FailErr(t, "writer foreign_keys",
		sqlDB.writer.QueryRowContext(t.Context(), `PRAGMA foreign_keys`).Scan(&writerForeignKeys))
	testutil.FailErr(t, "writer busy_timeout",
		sqlDB.writer.QueryRowContext(t.Context(), `PRAGMA busy_timeout`).Scan(&writerBusyTimeout))
	if writerForeignKeys != 1 || writerBusyTimeout != 10_000 {
		t.Fatalf("writer pragmas = foreign_keys:%d busy_timeout:%d", writerForeignKeys, writerBusyTimeout)
	}
}

func TestStoreSignalsStartupAndWritePressure(t *testing.T) {
	store := newStore(nil, nil)
	for name, signal := range map[string]<-chan struct{}{
		"maintenance": store.maintenanceSignals(),
		"checkpoint":  store.checkpointSignals(),
	} {
		select {
		case <-signal:
		default:
			t.Fatalf("missing %s startup signal", name)
		}
	}

	for range maintenanceWriteStride - 1 {
		store.noteWrite()
	}
	if len(store.maintenanceSignals()) != 0 || len(store.checkpointSignals()) != 0 {
		t.Fatal("store signaled before the write stride")
	}
	store.noteWrite()
	if len(store.maintenanceSignals()) != 1 || len(store.checkpointSignals()) != 1 {
		t.Fatal("store did not signal at the write stride")
	}
}

// BEGIN IMMEDIATE serializes concurrent read-then-write transactions.
func TestConcurrentReadThenWriteTransactionsSerialize(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "concurrency.db")
	sqlDB, err := Open(dbPath)
	testutil.FailErr(t, "db.Open failed", err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	stats := sqlDB.Stats()
	if stats.Writer.MaxOpenConnections != writerOpenConns || stats.Reader.MaxOpenConnections != readerOpenConns {
		t.Fatalf("pool limits = writer:%d reader:%d, want writer:%d reader:%d",
			stats.Writer.MaxOpenConnections, stats.Reader.MaxOpenConnections,
			writerOpenConns, readerOpenConns)
	}

	if _, err := sqlDB.ExecContext(ctx, `INSERT INTO store_meta(key, value) VALUES('counter', '0')`); err != nil {
		testutil.FailErr(t, "seed counter", err)
	}

	const goroutines = 64
	var wg sync.WaitGroup
	errs := make(chan error, goroutines)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tx, err := sqlDB.BeginTx(ctx, nil)
			if err != nil {
				errs <- fmt.Errorf("begin: %w", err)
				return
			}
			var current int
			if err := tx.QueryRowContext(ctx, `SELECT value FROM store_meta WHERE key='counter'`).Scan(&current); err != nil {
				_ = tx.Rollback()
				errs <- fmt.Errorf("read: %w", err)
				return
			}
			if _, err := tx.ExecContext(ctx, `UPDATE store_meta SET value=? WHERE key='counter'`, strconv.Itoa(current+1)); err != nil {
				_ = tx.Rollback()
				errs <- fmt.Errorf("write: %w", err)
				return
			}
			if err := tx.Commit(); err != nil {
				errs <- fmt.Errorf("commit: %w", err)
				return
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent read-then-write transaction failed (DB config cannot support the worker fan-out): %v", err)
	}

	var final int
	if err := sqlDB.QueryRowContext(ctx, `SELECT value FROM store_meta WHERE key='counter'`).Scan(&final); err != nil {
		testutil.FailErr(t, "read final", err)
	}
	if final != goroutines {
		t.Fatalf("counter = %d, want %d — increments were lost, transactions did not serialize correctly", final, goroutines)
	}

	testutil.FailErr(t, "close after concurrent commits", sqlDB.Close())
	sqlDB, err = Open(dbPath)
	testutil.FailErr(t, "reopen after concurrent commits", err)
	if err := sqlDB.QueryRowContext(ctx, `SELECT value FROM store_meta WHERE key='counter'`).Scan(&final); err != nil {
		testutil.FailErr(t, "read durable counter", err)
	}
	if final != goroutines {
		t.Fatalf("durable counter = %d, want %d", final, goroutines)
	}
	assertDatabaseIntegrity(t, sqlDB)
}

func TestPinnedReaderAndPassiveCheckpointsDoNotRejectWrites(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	sqlDB, err := Open(filepath.Join(t.TempDir(), "checkpoint-concurrency.db"))
	testutil.FailErr(t, "open", err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	_, err = sqlDB.ExecContext(ctx, `INSERT INTO store_meta(key, value) VALUES('counter', '0')`)
	testutil.FailErr(t, "seed", err)

	reader, err := sqlDB.reader.Conn(ctx)
	testutil.FailErr(t, "acquire pinned reader", err)
	defer func() { _ = reader.Close() }()
	_, err = reader.ExecContext(ctx, `BEGIN`)
	testutil.FailErr(t, "begin pinned reader", err)
	var snapshot int
	testutil.FailErr(t, "establish pinned snapshot",
		reader.QueryRowContext(ctx, `SELECT value FROM store_meta WHERE key='counter'`).Scan(&snapshot))

	const writes = 64
	errCh := make(chan error, writes+1)
	var writers sync.WaitGroup
	for i := range writes {
		writers.Add(1)
		go func() {
			defer writers.Done()
			if i%2 == 0 {
				_, writeErr := sqlDB.ExecContext(ctx,
					`UPDATE store_meta SET value = CAST(value AS INTEGER) + 1 WHERE key='counter'`)
				if writeErr != nil {
					errCh <- fmt.Errorf("direct write: %w", writeErr)
				}
				return
			}
			tx, txErr := sqlDB.BeginTx(ctx, nil)
			if txErr != nil {
				errCh <- fmt.Errorf("begin write: %w", txErr)
				return
			}
			defer func() { _ = tx.Rollback() }()
			if _, txErr = tx.ExecContext(ctx,
				`UPDATE store_meta SET value = CAST(value AS INTEGER) + 1 WHERE key='counter'`); txErr != nil {
				errCh <- fmt.Errorf("transaction write: %w", txErr)
				return
			}
			if txErr = tx.Commit(); txErr != nil {
				errCh <- fmt.Errorf("commit write: %w", txErr)
			}
		}()
	}

	checkpointDone := make(chan struct{})
	go func() {
		defer close(checkpointDone)
		for range writes {
			result, checkpointErr := CheckpointWAL(ctx, sqlDB)
			if checkpointErr != nil {
				errCh <- fmt.Errorf("passive checkpoint: %w", checkpointErr)
				return
			}
			if result.Mode != WALCheckpointPassive {
				errCh <- fmt.Errorf("checkpoint mode = %s", result.Mode)
				return
			}
		}
	}()

	writers.Wait()
	<-checkpointDone
	close(errCh)
	for concurrencyErr := range errCh {
		t.Fatal(concurrencyErr)
	}
	if snapshot != 0 {
		t.Fatalf("pinned reader snapshot = %d, want 0", snapshot)
	}
	var final int
	testutil.FailErr(t, "read final counter",
		sqlDB.QueryRowContext(ctx, `SELECT value FROM store_meta WHERE key='counter'`).Scan(&final))
	if final != writes {
		t.Fatalf("counter = %d, want %d", final, writes)
	}
	_, err = reader.ExecContext(ctx, `ROLLBACK`)
	testutil.FailErr(t, "release pinned reader", err)
}

func assertDatabaseIntegrity(t *testing.T, sqlDB DBTX) {
	t.Helper()
	var result string
	testutil.FailErr(t, "quick_check", sqlDB.QueryRowContext(t.Context(), `PRAGMA quick_check`).Scan(&result))
	if result != "ok" {
		t.Fatalf("quick_check = %q, want ok", result)
	}
	rows, err := sqlDB.QueryContext(t.Context(), `PRAGMA foreign_key_check`)
	testutil.FailErr(t, "foreign_key_check", err)
	defer func() { _ = rows.Close() }()
	if rows.Next() {
		t.Fatal("foreign_key_check found a violation")
	}
	testutil.FailErr(t, "iterate foreign_key_check", rows.Err())
}

func TestWriterAcknowledgementsUseSyncedWALCommits(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "durability.db"))
	testutil.FailErr(t, "open durable store", err)
	t.Cleanup(func() { _ = store.Close() })
	for range 2 {
		tx, err := store.BeginTx(t.Context(), nil)
		testutil.FailErr(t, "begin writer transaction", err)
		var synchronous int
		testutil.FailErr(t, "read writer durability", tx.QueryRowContext(t.Context(), "PRAGMA synchronous").Scan(&synchronous))
		if synchronous != 2 {
			_ = tx.Rollback()
			t.Fatalf("writer synchronous = %d, want FULL", synchronous)
		}
		_, err = tx.ExecContext(t.Context(), "INSERT OR REPLACE INTO store_meta(key,value) VALUES('durability_probe','accepted')")
		testutil.FailErr(t, "write receipt", err)
		testutil.FailErr(t, "commit synced transaction", tx.Commit())
	}
}
