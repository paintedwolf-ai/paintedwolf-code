package native

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
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

	tctx.Source.History = tools.SourceHistory{Files: st.History, Comparison: st.Comparisons, Git: st.Git, Authorship: st.Walk}
	tctx.Source.Commands = st.Commands
	tctx.Source.Observations = st.Inventory
	tctx.Source.GitMutations = st.Git
	tool := &WriteTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"path":    "a.go",
		"content": "package a\n",
	}, tctx)
	testutil.FailErr(t, "write tool failed", err)
	if _, err := os.Stat(filepath.Join(dir, "a.go")); err != nil {
		testutil.FailErr(t, "written file missing", err)
	}

	res, err := st.Walk.QueryWalk(t.Context(), "p1",
		sourceledger.Baseline{Kind: sourceledger.BaselineTurn, SessionID: "s1", Turn: 3},
		10, 0, sourceledger.CommitLens{})
	testutil.FailErr(t, "turn lens query failed", err)
	if len(res.Files) != 1 || res.Files[0].Path != "a.go" {
		t.Fatalf("turn 3 lens = %+v, want the file this turn wrote", res.Files)
	}

	// And the ordinal is addressed exactly: a neighbouring turn holds nothing.
	other, err := st.Walk.QueryWalk(t.Context(), "p1",
		sourceledger.Baseline{Kind: sourceledger.BaselineTurn, SessionID: "s1", Turn: 2},
		10, 0, sourceledger.CommitLens{})
	testutil.FailErr(t, "turn lens query failed", err)
	if len(other.Files) != 0 {
		t.Fatalf("turn 2 lens = %+v, want empty", other.Files)
	}
}

// A source-only repair must not require changing the command or its permissions.
func TestCommandFailureLoopAdmitsRecordedSourceRepair(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX test command fixture")
	}
	for _, mode := range []string{"foreground", "verification", "terminal"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			ledger := bindLedgerForWrites(t, dir)
			tctx := nativefixture.Context(dir)
			tctx.Identity.ProjectID = "p1"
			tctx.Source.SourceLedger = ledger
			tctx.Source.Observations = ledger.Inventory
			boundary := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true}, []sandbox.ToolProfile{{ID: tctx.Identity.Agent, Tools: map[string]bool{"command": true}}})
			runner := &command.CommandTool{Runner: hostcmd.NewRunner(), Boundary: boundary, Background: newTestBackgroundRegistry(t)}
			args := map[string]any{"command": "test -f ready.txt"}
			if mode == "terminal" {
				args["terminal_capture"] = map[string]any{}
			}
			if mode == "verification" {
				args["verification"] = true
			}
			for i := 0; i < 3; i++ {
				raw, err := runner.Run(t.Context(), args, tctx)
				testutil.FailErr(t, "failing process", err)
				var out map[string]any
				testutil.FailErr(t, "process result", json.Unmarshal([]byte(raw), &out))
				if out["ok"] != false {
					t.Fatalf("expected real failure: %s", raw)
				}
			}
			repochange.Advance(dir)
			_, err := runner.Run(t.Context(), args, tctx)
			var reject *toolrejection.ToolReject
			if !errors.As(err, &reject) || reject.Code != "COMMAND_FAILURE_LOOP" || reject.Data["failure_source_unchanged"] != true {
				t.Fatalf("watcher noise bypassed loop: %v", err)
			}
			write := &WriteTool{Boundary: nativefixture.Boundary(t)}
			_, err = write.Run(t.Context(), map[string]any{"path": "ready.txt", "content": "ready\n"}, tctx)
			testutil.FailErr(t, "recorded source repair", err)
			raw, err := runner.Run(t.Context(), args, tctx)
			testutil.FailErr(t, "unchanged command after repair", err)
			var out map[string]any
			testutil.FailErr(t, "repaired process result", json.Unmarshal([]byte(raw), &out))
			if out["ok"] != true {
				t.Fatalf("repaired process failed: %s", raw)
			}
		})
	}
}
