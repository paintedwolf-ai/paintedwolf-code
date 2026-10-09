package native

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
)

// bindLedgerForWrites builds a real ledger store over a temp DB whose project
// root is dir, so an agent write through the tool records a row the lens can read.
func bindLedgerForWrites(t *testing.T, dir string) *sourceledger.Store {
	t.Helper()
	sqlDB := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProject(t, sqlDB, "p1")
	_, err := sqlDB.ExecContext(t.Context(), `
		INSERT INTO project_roots (id, project_id, path, label, is_primary, added_at)
		VALUES ('r1', 'p1', ?, 'root', 1, ?)
	`, dir, db.FormatTime(time.Now().UTC()))
	testutil.FailErr(t, "insert root failed", err)
	st := sourceledger.New(sqlDB, t.TempDir())
	return st
}

// An agent write has to reach the ledger carrying the turn it ran under: a row
// landing on turn 0 makes the review lens, which asks for turn ≥ 1, answer empty.
// The ledger's own unit tests cannot see this — they pass Turn in by hand.
func TestAgentWriteStampsUserTurnOnLedgerRow(t *testing.T) {
	dir := t.TempDir()
	st := bindLedgerForWrites(t, dir)

	tctx := nativefixture.Context(dir)
	tctx.Identity.ProjectID = "p1"
	tctx.Identity.SessionID = "s1"
	tctx.Identity.UserTurn = 3
	tctx.Source.SourceLedger = st

	tool := &WriteTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"path":    "a.go",
		"content": "package a\n",
	}, tctx)
	testutil.FailErr(t, "write tool failed", err)
	if _, err := os.Stat(filepath.Join(dir, "a.go")); err != nil {
		testutil.FailErr(t, "written file missing", err)
	}

	res, err := st.QueryWalk(t.Context(), "p1",
		sourceledger.Baseline{Kind: sourceledger.BaselineTurn, SessionID: "s1", Turn: 3},
		10, 0, sourceledger.CommitLens{})
	testutil.FailErr(t, "turn lens query failed", err)
	if len(res.Files) != 1 || res.Files[0].Path != "a.go" {
		t.Fatalf("turn 3 lens = %+v, want the file this turn wrote", res.Files)
	}

	// And the ordinal is addressed exactly: a neighbouring turn holds nothing.
	other, err := st.QueryWalk(t.Context(), "p1",
		sourceledger.Baseline{Kind: sourceledger.BaselineTurn, SessionID: "s1", Turn: 2},
		10, 0, sourceledger.CommitLens{})
	testutil.FailErr(t, "turn lens query failed", err)
	if len(other.Files) != 0 {
		t.Fatalf("turn 2 lens = %+v, want empty", other.Files)
	}
}
