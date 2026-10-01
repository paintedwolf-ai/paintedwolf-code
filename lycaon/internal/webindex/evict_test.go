package webindex

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

// fillOverBudget returns a budget below the generated index size.
func fillOverBudget(t *testing.T, s *Store) int64 {
	t.Helper()
	big := strings.Repeat("steam machine coverage text ", 40)
	for i := 0; i < 400; i++ {
		s.QueuePage(t.Context(), Page{URL: fmt.Sprintf("https://a.example/p%d", i), Title: big})
	}
	s.Flush()
	size, err := fileBytes(t.Context(), s.db)
	testutil.FailErr(t, "file bytes", err)
	if size < 64<<10 {
		t.Fatalf("test corpus too small to evict: %d bytes", size)
	}
	return size / 4
}

func TestEvictOverByteBudgetStopsWhenDeletesCannotProgress(t *testing.T) {
	s := openTest(t)
	budget := fillOverBudget(t, s)

	// query_only keeps reads available while writes fail.
	_, err := s.db.ExecContext(t.Context(), "PRAGMA query_only = 1")
	testutil.FailErr(t, "query_only", err)

	done := make(chan error, 1)
	go func() { done <- s.evictOverByteBudget(t.Context(), s.db, budget) }()

	select {
	case evictErr := <-done:
		if evictErr == nil {
			t.Fatal("evictOverByteBudget returned nil; want a failure it can report")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("evictOverByteBudget did not return: the retention loop is unbounded")
	}
}

func TestEvictOverByteBudgetReportsAStall(t *testing.T) {
	s := openTest(t)
	budget := fillOverBudget(t, s)

	// The trigger cancels deletes without returning an error.
	_, err := s.db.ExecContext(t.Context(), `CREATE TRIGGER block_doc_deletes
		BEFORE DELETE ON docs BEGIN SELECT RAISE(IGNORE); END`)
	testutil.FailErr(t, "install blocking trigger", err)

	evictErr := s.evictOverByteBudget(t.Context(), s.db, budget)
	if !errors.Is(evictErr, errEvictionStalled) {
		t.Fatalf("err = %v want %v", evictErr, errEvictionStalled)
	}
}

func TestEvictionPassSurfacesFailure(t *testing.T) {
	s := openTest(t)
	fillOverBudget(t, s)
	_, err := s.db.ExecContext(t.Context(), "PRAGMA query_only = 1")
	testutil.FailErr(t, "query_only", err)

	if passErr := s.evictionPass(t.Context(), s.db, maxDocs, maxIndexBytes); passErr == nil {
		t.Fatal("evictionPass returned nil on a read-only store")
	}
}

func TestDrainPragmaDrainsAndReportsFailure(t *testing.T) {
	s := openTest(t)
	testutil.FailErr(t, "drain vacuum", drainPragma(t.Context(), s.db, "PRAGMA incremental_vacuum"))
	testutil.FailErr(t, "drain checkpoint", drainPragma(t.Context(), s.db, "PRAGMA wal_checkpoint(TRUNCATE)"))

	err := drainPragma(t.Context(), s.db, "PRAGMA wal_checkpoint(NOT_A_MODE")
	if err == nil {
		t.Fatal("drainPragma returned nil for a statement that cannot prepare")
	}
	if !strings.Contains(err.Error(), "wal_checkpoint") {
		t.Fatalf("err = %v want the statement named", err)
	}
}
