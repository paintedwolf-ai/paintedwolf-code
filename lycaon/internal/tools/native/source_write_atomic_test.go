package native

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/sourcefeed"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
	"github.com/lycaon/lycaon/pkg/api"
)

// txProbeSink records transactional staging and delivery.
type txProbeSink struct {
	mu        sync.Mutex
	events    []api.SourceChangesEvent
	delivered int
	failEmit  error
}

func (s *txProbeSink) SourceChanged(_ context.Context, ev api.SourceChangesEvent) error {
	s.mu.Lock()
	s.events = append(s.events, ev)
	s.mu.Unlock()
	return s.failEmit
}

func (s *txProbeSink) SourceChangedTx(ctx context.Context, tx *sql.Tx, ev api.SourceChangesEvent) error {
	if len(ev.Changes) != 1 {
		return errors.New("test probe expects one source change")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO source_feed_probe(path) VALUES(?)`, ev.Changes[0].Path); err != nil {
		return err
	}
	s.mu.Lock()
	s.events = append(s.events, ev)
	s.mu.Unlock()
	return s.failEmit
}

func (s *txProbeSink) Deliver() {
	s.mu.Lock()
	s.delivered++
	s.mu.Unlock()
}

func (s *txProbeSink) counts() (events, delivered int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.events), s.delivered
}

// failingRecordTx preserves the database and rejects capture.
type failingRecordTx struct{ *sourceledger.Store }

func (failingRecordTx) RecordTx(context.Context, *sql.Tx, sourceledger.RecordInput) error {
	return errSourceLedgerFixture
}

func bindProbeSink(t *testing.T, st *sourceledger.Store, sink *txProbeSink) {
	t.Helper()
	_, err := st.LedgerDB().ExecContext(t.Context(),
		`CREATE TABLE IF NOT EXISTS source_feed_probe(path TEXT NOT NULL)`)
	testutil.FailErr(t, "create probe table", err)
	t.Cleanup(sourcefeed.Bind(sink))
}

func probeRows(t *testing.T, st *sourceledger.Store) int {
	t.Helper()
	var n int
	err := st.LedgerDB().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM source_feed_probe`).Scan(&n)
	testutil.FailErr(t, "count probe rows", err)
	return n
}

func ledgerRows(t *testing.T, st *sourceledger.Store) int {
	t.Helper()
	var n int
	err := st.LedgerDB().QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM source_effects WHERE project_id = 'p1'`).Scan(&n)
	testutil.FailErr(t, "count source effects", err)
	return n
}

func TestAgentMutationCommitsAttributionAndEventTogether(t *testing.T) {
	dir := t.TempDir()
	st := bindLedgerForWrites(t, dir)
	sink := &txProbeSink{}
	bindProbeSink(t, st, sink)
	tctx := nativefixture.Context(dir)
	tctx.ProjectID, tctx.SessionID, tctx.UserTurn = "p1", "s1", 1
	tctx.SourceLedger = st
	tctx.SourceMutations = project.NewSourceMutationService(st.LedgerDB(), st)

	path := filepath.Join(dir, "one.txt")
	testutil.FailErr(t, "write", applyAgentFile(t.Context(), tctx, testMutationTarget(path), []byte("after\n"), nil, ""))

	if got := ledgerRows(t, st); got != 1 {
		t.Fatalf("ledger rows = %d, want 1", got)
	}
	if got := probeRows(t, st); got != 1 {
		t.Fatalf("event rows staged in the mutation transaction = %d, want 1", got)
	}
	events, delivered := sink.counts()
	if events != 1 || delivered != 1 {
		t.Fatalf("events=%d delivered=%d, want 1 and 1 — Deliver runs once the commit lands", events, delivered)
	}
}

// The ledger and event roll back together.
func TestAgentMutationEventFailureRollsBackTheLedgerRow(t *testing.T) {
	dir := t.TempDir()
	st := bindLedgerForWrites(t, dir)
	sink := &txProbeSink{failEmit: errors.New("outbox unavailable")}
	bindProbeSink(t, st, sink)
	tctx := nativefixture.Context(dir)
	tctx.ProjectID, tctx.SessionID, tctx.UserTurn = "p1", "s1", 1
	tctx.SourceLedger = st
	tctx.SourceMutations = project.NewSourceMutationService(st.LedgerDB(), st)

	path := filepath.Join(dir, "torn.txt")
	err := applyAgentFile(t.Context(), tctx, testMutationTarget(path), []byte("after\n"), nil, "")
	if err == nil {
		t.Fatal("announce failure returned no error")
	}
	if got := ledgerRows(t, st); got != 0 {
		t.Fatalf("ledger rows = %d after a failed announce, want 0", got)
	}
	if got := probeRows(t, st); got != 0 {
		t.Fatalf("staged event rows = %d after a failed announce, want 0", got)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		testutil.FailErr(t, "landed write missing", statErr)
	}
	if _, delivered := sink.counts(); delivered != 0 {
		t.Fatalf("delivery woken %d times for a rolled-back commit, want 0", delivered)
	}
}

// Capture failure leaves no event.
func TestAgentMutationLedgerFailureAnnouncesNothing(t *testing.T) {
	dir := t.TempDir()
	st := bindLedgerForWrites(t, dir)
	sink := &txProbeSink{}
	bindProbeSink(t, st, sink)
	tctx := nativefixture.Context(dir)
	tctx.ProjectID, tctx.SessionID, tctx.UserTurn = "p1", "s1", 1
	tctx.SourceLedger = failingRecordTx{Store: st}

	path := filepath.Join(dir, "unrecorded.txt")
	err := applyAgentFile(t.Context(), tctx, testMutationTarget(path), []byte("after\n"), nil, "")
	if !errors.Is(err, errSourceLedgerFixture) {
		t.Fatalf("error = %v, want the ledger failure", err)
	}
	if got := ledgerRows(t, st); got != 0 {
		t.Fatalf("ledger rows = %d, want 0", got)
	}
	if got := probeRows(t, st); got != 0 {
		t.Fatalf("staged event rows = %d for a refused capture, want 0", got)
	}
	events, delivered := sink.counts()
	if events != 0 || delivered != 0 {
		t.Fatalf("events=%d delivered=%d for a refused capture, want 0 and 0", events, delivered)
	}
}

func TestAgentMutationPreparationFailureLeavesFileUntouched(t *testing.T) {
	dir := t.TempDir()
	st := bindLedgerForWrites(t, dir)
	tctx := nativefixture.Context(dir)
	tctx.ProjectID, tctx.SessionID = "p1", "s1"
	tctx.SourceLedger = st
	tctx.SourceMutations = project.NewSourceMutationService(st.LedgerDB(), st)
	_, err := st.LedgerDB().ExecContext(t.Context(), `CREATE TRIGGER reject_effect_preparation BEFORE INSERT ON source_mutations BEGIN SELECT RAISE(ABORT,'journal unavailable'); END`)
	testutil.FailErr(t, "inject journal failure", err)
	path := filepath.Join(dir, "not-created.txt")
	err = applyAgentFile(t.Context(), tctx, testMutationTarget(path), []byte("after"), nil, "")
	if err == nil {
		t.Fatal("write succeeded without preparing its journal")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("file changed despite preparation failure: %v", err)
	}
}
