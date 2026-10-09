package session

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/session/workerworkspace"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourcefeed"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/workspace"
	"github.com/lycaon/lycaon/pkg/api"
)

type workerBranchLedgerCapture struct {
	mu      sync.Mutex
	records []sourceledger.RecordInput
}

func (c *workerBranchLedgerCapture) Record(_ context.Context, in sourceledger.RecordInput) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.records = append(c.records, in)
	return nil
}

func (c *workerBranchLedgerCapture) RecordTx(ctx context.Context, _ *sql.Tx, in sourceledger.RecordInput) error {
	return c.Record(ctx, in)
}

type workerBranchFeedCapture struct {
	mu     sync.Mutex
	events []api.SourceChangesEvent
}

func (c *workerBranchFeedCapture) SourceChanged(_ context.Context, ev api.SourceChangesEvent) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, ev)
	return nil
}

func (c *workerBranchFeedCapture) SourceChangedTx(ctx context.Context, _ *sql.Tx, ev api.SourceChangesEvent) error {
	return c.SourceChanged(ctx, ev)
}

func (c *workerBranchFeedCapture) Deliver() {}

// A write worker's edit lands in its branch tree and records against the
// worker branch at the project root path the branch mirrors.
func TestWorkerBranchEditRecordsAgainstWorkerBranch(t *testing.T) {
	cases := []struct {
		name      string
		roots     func(base string) []projectroot.RootRef
		editRoot  string
		modelPath string
	}{
		{
			name: "single root",
			roots: func(base string) []projectroot.RootRef {
				return []projectroot.RootRef{{ID: "primary", Label: "app", Path: filepath.Join(base, "app"), IsPrimary: true}}
			},
			editRoot:  "primary",
			modelPath: "internal/app/routes.go",
		},
		{
			name: "multi root",
			roots: func(base string) []projectroot.RootRef {
				return []projectroot.RootRef{
					{ID: "primary", Label: "app", Path: filepath.Join(base, "app"), IsPrimary: true},
					{ID: "secondary", Label: "lib", Path: filepath.Join(base, "lib")},
				}
			},
			editRoot:  "secondary",
			modelPath: "@lib/internal/app/routes.go",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			base := t.TempDir()
			roots := tc.roots(base)
			for _, root := range roots {
				testutil.FailErr(t, "mkdir root", os.MkdirAll(filepath.Join(root.Path, "internal", "app"), 0o755))
				testutil.FailErr(t, "write root file", os.WriteFile(filepath.Join(root.Path, "internal", "app", "routes.go"), []byte("package app\n"), 0o644))
			}
			const jobID = "job-worker-edit"
			manager := workspace.NewManager(filepath.Join(base, "branches"), filepath.Join(base, "seeds"))
			binding, _, err := manager.CreateWorkerWorkspaceFromSources(ctx, roots, roots, "primary", jobID)
			testutil.FailErr(t, "create worker branch", err)
			branch, err := newBranchWorkspace(binding.Root)
			testutil.FailErr(t, "new branch workspace", err)
			tctx, err := workerworkspace.New(nil, nil, newBranchWorkspace).EnsureBranch(ctx, tools.ToolContext{
				WorkerBranchRoot: binding.Root, BranchWorkspace: branch,
			})
			testutil.FailErr(t, "ensure worker branch", err)

			ledger := &workerBranchLedgerCapture{}
			feed := &workerBranchFeedCapture{}
			t.Cleanup(sourcefeed.Bind(feed))
			tctx.ProjectID = "project-worker-edit"
			tctx.SessionID = "worker-session"
			tctx.ActiveRootID = "primary"
			tctx.WorkerJobID = jobID
			tctx.SourceWorkspaceKind = api.SourceWorkspaceKindWorker
			tctx.SourceLedger = ledger
			tctx.Agent = "implementer"
			tctx.ToolCallID = "call-edit"

			boundary := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true, RejectSymlinkEscape: true},
				[]sandbox.ToolProfile{{ID: "implementer", Tools: map[string]bool{"edit": true}}})
			_, err = (&native.EditTool{Boundary: boundary}).Run(ctx, map[string]any{
				"path": tc.modelPath, "old_string": "package app\n", "new_string": "package routes\n",
			}, tctx)
			testutil.FailErr(t, "worker edit", err)

			wantBranch, err := sourcebranch.ForWorker(jobID)
			testutil.FailErr(t, "worker branch id", err)
			if len(ledger.records) != 1 {
				t.Fatalf("ledger records = %+v, want one worker branch record", ledger.records)
			}
			record := ledger.records[0]
			if record.BranchID != wantBranch || record.RootID != tc.editRoot || record.Path != "internal/app/routes.go" ||
				record.JobID != jobID || record.SessionID != "worker-session" || record.Op != api.SourceChangeOpWrite {
				t.Fatalf("ledger record = %+v", record)
			}
			if len(feed.events) != 1 || len(feed.events[0].Changes) != 1 {
				t.Fatalf("source_changed events = %+v, want one worker change", feed.events)
			}
			event := feed.events[0]
			change := event.Changes[0]
			if event.WorkspaceKind != api.SourceWorkspaceKindWorker || change.WorkerID != jobID ||
				change.SessionID != "worker-session" || change.RootID != tc.editRoot || change.Path != "internal/app/routes.go" {
				t.Fatalf("source_changed event = %+v change = %+v", event, change)
			}
			for _, root := range roots {
				data, readErr := os.ReadFile(filepath.Join(root.Path, "internal", "app", "routes.go"))
				testutil.FailErr(t, "read project file", readErr)
				if string(data) != "package app\n" {
					t.Fatalf("project root %s changed: %q", root.ID, data)
				}
			}
		})
	}
}
