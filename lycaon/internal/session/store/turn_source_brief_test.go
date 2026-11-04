package store

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type turnSourceBriefTestStore interface {
	turnClockTestStore
	PutTurnSourceBrief(ctx context.Context, sessionID, openingMessageID string, mint evidence.SourceChangeMint) error
	TurnSourceBriefs(ctx context.Context, sessionID string) (map[string]string, error)
	LoadLedger(ctx context.Context, sessionID string) (evidence.Ledger, error)
}

func turnSourceBriefStores() []struct {
	name string
	open func(*testing.T) turnSourceBriefTestStore
} {
	return []struct {
		name string
		open func(*testing.T) turnSourceBriefTestStore
	}{
		{name: "memory", open: func(*testing.T) turnSourceBriefTestStore { return NewMemory() }},
		{name: "sql", open: func(t *testing.T) turnSourceBriefTestStore { return openTurnSQLStore(t).(turnSourceBriefTestStore) }},
	}
}

// briefWithRows mints one record per row from the ordinal the store gave it.
func briefWithRows(t *testing.T, briefJSON string, rows int) evidence.SourceChangeMint {
	t.Helper()
	return func(first int) (string, []evidence.Record, error) {
		records := make([]evidence.Record, 0, rows)
		for i := range rows {
			records = append(records, evidence.NewSourceChangeRecord(first+i))
		}
		return briefJSON, records, nil
	}
}

// A turn's brief is written once, when the turn opens; a later write for the
// same turn keeps the first, and a message that opened no turn takes none.
func TestTurnSourceBriefIsFixedWhenTheTurnOpensParity(t *testing.T) {
	for _, fixture := range turnSourceBriefStores() {
		t.Run(fixture.name, func(t *testing.T) {
			ctx := t.Context()
			st := fixture.open(t)
			sess, err := st.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
			testutil.FailErr(t, "create session", err)
			first := appendUserPrompt(t, st, sess.ID, "first")
			second := appendUserPrompt(t, st, sess.ID, "second")

			none, err := st.TurnSourceBriefs(ctx, sess.ID)
			testutil.FailErr(t, "list before any brief", err)
			if len(none) != 0 {
				t.Fatalf("briefs before any write = %v", none)
			}
			testutil.FailErr(t, "put second brief", st.PutTurnSourceBrief(ctx, sess.ID, second, briefWithRows(t, `{"other_files":2}`, 0)))
			testutil.FailErr(t, "rewrite second brief", st.PutTurnSourceBrief(ctx, sess.ID, second, briefWithRows(t, `{"other_files":9}`, 0)))
			testutil.FailErr(t, "put brief for no turn", st.PutTurnSourceBrief(ctx, sess.ID, "not-a-turn", briefWithRows(t, `{"other_files":1}`, 0)))
			testutil.FailErr(t, "put empty brief", st.PutTurnSourceBrief(ctx, sess.ID, first, briefWithRows(t, " ", 0)))

			got, err := st.TurnSourceBriefs(ctx, sess.ID)
			testutil.FailErr(t, "list briefs", err)
			if len(got) != 1 || got[second] != `{"other_files":2}` {
				t.Fatalf("briefs = %v, want only the second turn's first write", got)
			}
		})
	}
}

// AC-Q-01: rows are citable from the moment the brief exists, each handle is
// minted once, and a row never counts as having observed the file.
func TestTurnSourceBriefMintsCitableRecordsParity(t *testing.T) {
	for _, fixture := range turnSourceBriefStores() {
		t.Run(fixture.name, func(t *testing.T) {
			ctx := t.Context()
			st := fixture.open(t)
			sess, err := st.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
			testutil.FailErr(t, "create session", err)
			first := appendUserPrompt(t, st, sess.ID, "first")
			second := appendUserPrompt(t, st, sess.ID, "second")

			testutil.FailErr(t, "put first brief", st.PutTurnSourceBrief(ctx, sess.ID, first, briefWithRows(t, `{"other_files":1}`, 2)))
			testutil.FailErr(t, "rewrite first brief", st.PutTurnSourceBrief(ctx, sess.ID, first, briefWithRows(t, `{"other_files":2}`, 5)))
			testutil.FailErr(t, "put brief for no turn", st.PutTurnSourceBrief(ctx, sess.ID, "not-a-turn", briefWithRows(t, `{"other_files":3}`, 5)))
			testutil.FailErr(t, "put second brief", st.PutTurnSourceBrief(ctx, sess.ID, second, briefWithRows(t, `{"other_files":4}`, 1)))

			ledger, err := st.LoadLedger(ctx, sess.ID)
			testutil.FailErr(t, "load ledger", err)
			if len(ledger.Handles) != 3 {
				t.Fatalf("handles = %v, want one per recorded row", evidence.HandlesSorted(ledger))
			}
			for _, handle := range []string{"source_change#1", "source_change#2", "source_change#3"} {
				rec, ok := evidence.ResolveHandle(ledger, handle)
				if !ok {
					t.Fatalf("%s missing from %v", handle, evidence.HandlesSorted(ledger))
				}
				if rec.Kind != evidence.KindSourceChange || rec.Shape != evidence.ShapeStructuredEvent || rec.Fidelity != evidence.FidelityStructured {
					t.Fatalf("%s = %+v", handle, rec)
				}
				if rec.Path != "" || len(rec.LineRanges) != 0 || rec.Survey || len(rec.Body) != 0 {
					t.Fatalf("%s must observe no file: %+v", handle, rec)
				}
			}
			if len(ledger.ByPath) != 0 {
				t.Fatalf("rows entered the path index: %v", ledger.ByPath)
			}
		})
	}
}

// A brief that cannot mint is not recorded: no row may name a handle with no record.
func TestTurnSourceBriefMintFailureRecordsNothingParity(t *testing.T) {
	for _, fixture := range turnSourceBriefStores() {
		t.Run(fixture.name, func(t *testing.T) {
			ctx := t.Context()
			st := fixture.open(t)
			sess, err := st.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
			testutil.FailErr(t, "create session", err)
			opening := appendUserPrompt(t, st, sess.ID, "first")
			mintErr := errors.New("mint failed")
			err = st.PutTurnSourceBrief(ctx, sess.ID, opening, func(int) (string, []evidence.Record, error) {
				return "", nil, mintErr
			})
			if !errors.Is(err, mintErr) {
				t.Fatalf("err = %v, want %v", err, mintErr)
			}
			briefs, err := st.TurnSourceBriefs(ctx, sess.ID)
			testutil.FailErr(t, "list briefs", err)
			ledger, err := st.LoadLedger(ctx, sess.ID)
			testutil.FailErr(t, "load ledger", err)
			if len(briefs) != 0 || len(ledger.Handles) != 0 {
				t.Fatalf("briefs = %v handles = %v, want neither", briefs, evidence.HandlesSorted(ledger))
			}
		})
	}
}
