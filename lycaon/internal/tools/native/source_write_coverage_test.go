package native

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/project"
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
			tctx.ProjectID = "p1"
			tctx.SessionID = "s1"
			tctx.UserTurn = 4
			tctx.ToolCallID = "call_" + tc.tool
			tctx.SourceLedger = st
			tctx.SourceMutations = project.NewSourceMutationService(st.LedgerDB(), st)

			testutil.FailErr(t, tc.tool+" tool failed", tc.run(t, dir, tctx))

			// The session lens is the reader's view. A row that missed the
			// session or the turn is a change nothing can show.
			res, err := st.QueryWalk(t.Context(), "p1",
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
