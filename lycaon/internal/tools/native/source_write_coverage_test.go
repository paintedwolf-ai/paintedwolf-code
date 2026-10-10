package native

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
	"github.com/lycaon/lycaon/pkg/api"
)

// Every mutating native tool lands an attributed row. A write without one reaches
// the file watcher as an external change, with no session, turn, or pre-image.
func TestEveryMutatingToolRecordsAnAttributedRow(t *testing.T) {
	cases := []struct {
		tool string
		// seed prepares the workspace; run drives the tool.
		seed func(t *testing.T, dir string)
		run  func(t *testing.T, dir string, tctx tools.ToolContext) error
		path string
		op   api.SourceChangeOp
	}{
		{
			tool: "write",
			run: func(t *testing.T, dir string, tctx tools.ToolContext) error {
				_, err := (&WriteTool{Boundary: nativefixture.Boundary(t)}).Run(context.Background(),
					map[string]any{"path": "a.go", "content": "package a\n"}, tctx)
				return err
			},
			path: "a.go", op: api.SourceChangeOpCreate,
		},
		{
			tool: "replace_lines",
			seed: func(t *testing.T, dir string) { seedFile(t, dir, "a.go", "one\ntwo\nthree\n") },
			run: func(t *testing.T, dir string, tctx tools.ToolContext) error {
				_, err := (&ReplaceLinesTool{Boundary: nativefixture.Boundary(t)}).Run(context.Background(),
					map[string]any{
						"path": "a.go", "start_line": 2, "end_line": 2,
						"new_content": "TWO\n",
					}, tctx)
				return err
			},
			path: "a.go", op: api.SourceChangeOpWrite,
		},
		{
			tool: "copy",
			seed: func(t *testing.T, dir string) { seedFile(t, dir, "a.go", "package a\n") },
			run: func(t *testing.T, dir string, tctx tools.ToolContext) error {
				_, err := (&CopyTool{Boundary: nativefixture.Boundary(t)}).Run(context.Background(),
					map[string]any{"copies": []any{map[string]any{"from": "a.go", "to": "b.go"}}}, tctx)
				return err
			},
			path: "b.go", op: api.SourceChangeOpCreate,
		},
		{
			tool: "move",
			seed: func(t *testing.T, dir string) { seedFile(t, dir, "a.go", "package a\n") },
			run: func(t *testing.T, dir string, tctx tools.ToolContext) error {
				_, err := (&MoveTool{Boundary: nativefixture.Boundary(t)}).Run(context.Background(),
					map[string]any{"moves": []any{map[string]any{"from": "a.go", "to": "b.go"}}}, tctx)
				return err
			},
			path: "b.go", op: api.SourceChangeOpRename,
		},
		{
			tool: "delete",
			seed: func(t *testing.T, dir string) { seedFile(t, dir, "a.go", "package a\n") },
			run: func(t *testing.T, dir string, tctx tools.ToolContext) error {
				_, err := (&DeleteTool{Boundary: nativefixture.Boundary(t)}).Run(context.Background(),
					map[string]any{"paths": []any{"a.go"}}, tctx)
				return err
			},
			path: "a.go", op: api.SourceChangeOpDelete,
		},
		{
			// Metadata mutations land as zero-diff write revisions so the
			// review lens still sees the touch.
			tool: "chmod",
			seed: func(t *testing.T, dir string) { seedFile(t, dir, "run.sh", "#!/bin/sh\n") },
			run: func(t *testing.T, dir string, tctx tools.ToolContext) error {
				_, err := (&ChmodTool{Boundary: nativefixture.Boundary(t)}).Run(context.Background(),
					map[string]any{"paths": []any{"run.sh"}, "mode": "+x"}, tctx)
				return err
			},
			path: "run.sh", op: api.SourceChangeOpWrite,
		},
		{
			tool: "chown",
			seed: func(t *testing.T, dir string) { seedFile(t, dir, "a.go", "package a\n") },
			run: func(t *testing.T, dir string, tctx tools.ToolContext) error {
				if !chownSupported() {
					t.Skip("chown unsupported on this platform")
				}
				_, err := (&ChownTool{Boundary: nativefixture.Boundary(t)}).Run(context.Background(),
					map[string]any{"paths": []any{"a.go"}, "owner": "current"}, tctx)
				return err
			},
			path: "a.go", op: api.SourceChangeOpWrite,
		},
	}

	for _, tc := range cases {
		t.Run(tc.tool, func(t *testing.T) {
			dir := t.TempDir()
			if tc.seed != nil {
				tc.seed(t, dir)
			}
			st := bindLedgerForWrites(t, dir)
			tctx := nativefixture.Context(dir)
			tctx.Identity.ProjectID = "p1"
			tctx.Identity.SessionID = "s1"
			tctx.Identity.UserTurn = 4
			tctx.Identity.ToolCallID = "call_" + tc.tool
			tctx.Source.SourceLedger = st
			tctx.Source.History = tools.SourceHistory{Files: st.History, Comparison: st.Comparisons, Git: st.Git, Authorship: st.Walk}
			tctx.Source.Commands = st.Commands
			tctx.Source.Observations = st.Inventory
			tctx.Source.GitMutations = st.Git
			tctx.Source.SourceMutations = projectsource.NewSourceMutationService(st.LedgerDB(), st)

			testutil.FailErr(t, tc.tool+" tool failed", tc.run(t, dir, tctx))

			// The session lens is the reader's view. A row that missed the
			// session or the turn is a change nothing can show.
			res, err := st.Walk.QueryWalk(t.Context(), "p1",
				sourceledger.Baseline{Kind: sourceledger.BaselineTurn, SessionID: "s1", Turn: 4},
				20, 0, sourceledger.CommitLens{})
			testutil.FailErr(t, "turn lens query failed", err)

			var found *sourceledger.WalkFile
			for i := range res.Files {
				if res.Files[i].Path == tc.path {
					found = &res.Files[i]
				}
			}
			if found == nil {
				t.Fatalf("%s left no attributed row for %s — lens holds %+v", tc.tool, tc.path, res.Files)
			}
			row := found.Effects[0]
			if row.Op != tc.op {
				t.Fatalf("op = %q, want %q", row.Op, tc.op)
			}
			if row.Origin != api.SourceChangeOriginAgent {
				t.Fatalf("origin = %q — an unattributed row is what the watcher writes, not a tool", row.Origin)
			}
			if row.ToolCallID != "call_"+tc.tool {
				t.Fatalf("tool_call_id = %q, want the call that wrote it", row.ToolCallID)
			}
		})
	}
}

func seedFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	full := filepath.Join(dir, rel)
	testutil.FailErr(t, "mkdir seed", os.MkdirAll(filepath.Dir(full), 0o755))
	testutil.FailErr(t, "seed file", os.WriteFile(full, []byte(content), 0o644))
}

type blueprintObserverFunc func(context.Context, string, string)

func (f blueprintObserverFunc) AfterWrite(ctx context.Context, sessionID, path string) {
	f(ctx, sessionID, path)
}

func TestBlueprintObserverReleaseDrainsCopiedWriteFencing(t *testing.T) {
	entered, canceled, resume, finished := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(resume) })
	oldRelease := SetBlueprintWriteObserver(blueprintObserverFunc(func(ctx context.Context, sessionID, path string) {
		if sessionID != "session" || path != "blueprint.md" {
			t.Errorf("write subject = %q/%q", sessionID, path)
		}
		close(entered)
		<-ctx.Done()
		close(canceled)
		<-resume
	}))
	t.Cleanup(func() { unblock(); testutil.FailErr(t, "drain old blueprint observer", oldRelease(t.Context())) })
	tctx := tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: " session "}}
	go func() {
		notifyBlueprintWrite(t.Context(), tctx, "", ".", " blueprint.md ")
		close(finished)
	}()
	<-entered
	var observed []string
	currentRelease := SetBlueprintWriteObserver(blueprintObserverFunc(func(_ context.Context, sessionID, path string) {
		observed = append(observed, sessionID+":"+path)
	}))
	t.Cleanup(func() { testutil.FailErr(t, "drain current blueprint observer", currentRelease(t.Context())) })
	deadline, cancel := context.WithCancel(t.Context())
	cancel()
	if err := oldRelease(deadline); !errors.Is(err, context.Canceled) {
		t.Fatalf("unfinished observer release = %v, want cancellation evidence", err)
	}
	<-canceled
	select {
	case <-finished:
		t.Fatal("release reported drained before the copied write callback returned")
	default:
	}
	notifyBlueprintWrite(t.Context(), tctx, "current.md")
	if len(observed) != 1 || observed[0] != "session:current.md" {
		t.Fatalf("old owner release changed current write fencing: %v", observed)
	}
	unblock()
	<-finished
	testutil.FailErr(t, "retry old observer drain", oldRelease(t.Context()))
	notifyBlueprintWrite(t.Context(), tctx, "next.md")
	if len(observed) != 2 || observed[1] != "session:next.md" {
		t.Fatalf("completed old drain erased replacement: %v", observed)
	}
	testutil.FailErr(t, "release current blueprint observer", currentRelease(t.Context()))
	notifyBlueprintWrite(t.Context(), tctx, "closed.md")
	if len(observed) != 2 {
		t.Fatal("closed owner admitted a new write callback")
	}
}

func TestNativeWriteDeliversActiveBlueprintObserver(t *testing.T) {
	dir := t.TempDir()
	ledger := bindLedgerForWrites(t, dir)
	tctx := nativefixture.Context(dir)
	tctx.Identity.ProjectID, tctx.Identity.SessionID, tctx.Identity.ToolCallID = "p1", "session", "write"
	tctx.Source.SourceLedger = ledger
	tctx.Source.History = tools.SourceHistory{Files: ledger.History, Comparison: ledger.Comparisons, Git: ledger.Git, Authorship: ledger.Walk}
	tctx.Source.Commands, tctx.Source.Observations, tctx.Source.GitMutations = ledger.Commands, ledger.Inventory, ledger.Git
	tctx.Source.SourceMutations = projectsource.NewSourceMutationService(ledger.LedgerDB(), ledger)
	var subjects []string
	release := SetBlueprintWriteObserver(blueprintObserverFunc(func(_ context.Context, sessionID, path string) {
		subjects = append(subjects, sessionID+":"+path)
	}))
	t.Cleanup(func() { testutil.FailErr(t, "release native write observer", release(t.Context())) })
	_, err := (&WriteTool{Boundary: nativefixture.Boundary(t)}).Run(t.Context(), map[string]any{"path": "blueprint.md", "content": "# Plan"}, tctx)
	testutil.FailErr(t, "write bound blueprint", err)
	if len(subjects) != 1 || subjects[0] != "session:blueprint.md" {
		t.Fatalf("successful native write missed its active blueprint fence: %v", subjects)
	}
}
