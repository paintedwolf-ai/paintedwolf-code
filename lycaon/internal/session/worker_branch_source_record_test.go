package session

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/inspector"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/session/workerworkspace"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourcefeed"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/verification"
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

func (c *workerBranchLedgerCapture) Prepare(_ context.Context, inputs []sourceledger.RecordInput) (sourceledger.PreparedRecording, error) {
	return preparedCapture(func(ctx context.Context, _ *sql.Tx) (sourceledger.TrackedFile, error) {
		for _, in := range inputs {
			if err := c.Record(ctx, in); err != nil {
				return sourceledger.TrackedFile{}, err
			}
		}
		return sourceledger.TrackedFile{}, nil
	}), nil
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
			tctx, err := workerworkspace.New(nil, nil, newBranchWorkspace).EnsureBranch(ctx, tools.ToolContext{Source: tools.InvocationSource{WorkerBranchRoot: binding.Root, BranchWorkspace: branch}})
			testutil.FailErr(t, "ensure worker branch", err)

			ledger := &workerBranchLedgerCapture{}
			feed := &workerBranchFeedCapture{}
			t.Cleanup(sourcefeed.Bind(feed))
			tctx.Identity.ProjectID = "project-worker-edit"
			tctx.Identity.SessionID = "worker-session"
			tctx.Source.ActiveRootID = "primary"
			tctx.Identity.WorkerJobID = jobID
			tctx.Source.SourceWorkspaceKind = api.SourceWorkspaceKindWorker
			tctx.Source.SourceLedger = ledger
			tctx.Identity.Agent = "implementer"
			tctx.Identity.ToolCallID = "call-edit"

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

type preparedCapture func(context.Context, *sql.Tx) (sourceledger.TrackedFile, error)

func (p preparedCapture) CommitTx(ctx context.Context, tx *sql.Tx) (sourceledger.TrackedFile, error) {
	return p(ctx, tx)
}
func (p preparedCapture) Close() {}

type noopWorkerBranchClaim struct{}

func (noopWorkerBranchClaim) ClaimWorkerBranch(context.Context, string) (*api.WorkerTask, error) {
	return nil, fmt.Errorf("worker branch claim not available")
}

type failingWorkerModelStore struct {
	*store.Memory
	err error
}

func (s *failingWorkerModelStore) UpdateSession(context.Context, string, func(*api.Session)) error {
	return s.err
}

func TestWorkerModelSaveFailureRemovesPartialChild(t *testing.T) {
	st := &failingWorkerModelStore{Memory: store.NewMemory(), err: errors.New("model save failed")}
	policy := llm.NewInMemoryPolicyStore(llm.ModelPolicy{
		Coordinator: llm.ModelRef{ProviderID: "fixture", Model: "fixture"},
	})
	manager := NewHost(st, Models{Client: nil, Provider: &llm.Service{Router: llm.NewStaticModelRouter(policy)}, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	parent, err := st.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create worker parent", err)
	child, err := manager.Workers.SpawnChild(t.Context(), parent.ID, api.SpawnChildRequest{AgentType: "implement"})
	if child != nil || !errors.Is(err, st.err) {
		t.Fatalf("spawn = %+v, %v; want no child and save failure", child, err)
	}
	sessions, err := st.List(t.Context())
	testutil.FailErr(t, "list after failed assignment", err)
	if len(sessions) != 1 || sessions[0].ID != parent.ID {
		t.Fatalf("failed assignment retained a partial worker: %+v", sessions)
	}
}

func TestSpawnedWorkerKeepsPoolAssignmentAfterReload(t *testing.T) {
	database := testdbfixture.Open(t, "worker-model.db")
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, t.TempDir())
	st := store.NewSQL(database)
	policy := llm.NewInMemoryPolicyStore(llm.ModelPolicy{
		Coordinator: llm.ModelRef{ProviderID: "coordinator", Model: "coordinator-model"},
		AgentPool: llm.AgentPool{Selection: llm.PoolSelectionRoundRobin, Models: []llm.ModelRef{
			{ProviderID: "first-provider", Model: "first-model"},
			{ProviderID: "second-provider", Model: "second-model"},
		}},
	})
	manager := NewHost(st, Models{Client: nil, Provider: &llm.Service{Policy: policy, Router: llm.NewStaticModelRouter(policy)}, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	parent, err := st.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create worker parent", err)
	for i, provider := range []string{"first-provider", "second-provider", "first-provider"} {
		child, err := manager.Workers.SpawnChild(t.Context(), parent.ID, api.SpawnChildRequest{AgentType: "implement", Prompt: "inspect the fixture"})
		testutil.FailErr(t, "spawn assigned worker", err)
		if child.ProviderID != provider || child.Model == "" {
			t.Fatalf("worker %d assignment = %s/%s, want provider %s", i, child.ProviderID, child.Model, provider)
		}
		loaded, err := store.NewSQL(database).Get(t.Context(), child.ID)
		testutil.FailErr(t, "reload assigned worker", err)
		assertWorkerRoutingStable(t, llm.NewStaticModelRouter(policy), loaded, child)
	}
}

func assertWorkerRoutingStable(t *testing.T, router *llm.StaticModelRouter, loaded, child *api.Session) {
	t.Helper()
	for range 4 {
		got, err := router.WithOverlayRoots(nil).ResolveSession(t.Context(), loaded)
		testutil.FailErr(t, "resolve reloaded worker", err)
		if got.ProviderID != child.ProviderID || got.Model != child.Model {
			t.Fatalf("worker assignment changed after reload: %+v, want %s/%s", got, child.ProviderID, child.Model)
		}
	}
}

type pendingOutcomeQueue struct {
	stubWorkerCycleLister
	pending []api.WorkerTask
}

func (q *pendingOutcomeQueue) ListPendingOutcomes(context.Context) ([]api.WorkerTask, error) {
	return q.pending, nil
}

func TestWorkerSynthesisSettlesAfterOutcomeAcknowledgement(t *testing.T) {
	ctx := t.Context()
	st := store.NewMemory()
	mgr := NewHost(st, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	sess, err := st.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	testutil.FailErr(t, "mark visible turn busy", st.SetSessionStatus(ctx, sess.ID, api.SessionStatusBusy))
	q := &pendingOutcomeQueue{pending: []api.WorkerTask{{
		ID: "job-a", ProjectID: sess.ProjectID, ParentSessionID: sess.ID, Status: api.WorkerStatusComplete,
	}}}
	mgr.SetWorkerQueue(q)
	loop := mgr.Coordinator.Runtime.CoordinatorLoop()
	finishExecution := loop.Admission.BeginPromptExecution(t.Context(), sess.ID)
	defer finishExecution()
	loop.Nudges.Nudge(ctx, sess.ID, anchor.PhaseAdvanced, anchor.PhaseAdvanced, "", anchor.Envelope{})
	loop.Cycles.OnWorkerCycleTerminal(ctx, sess.ID, "job-a")
	if !loop.Nudges.HasPendingLoopWakes(sess.ID) {
		t.Fatal("phase wake must wait for outcome acknowledgement")
	}

	q.pending = nil
	mgr.Runner.Settlement.Begin(sess.ID, "")
	testutil.FailErr(t, "finish worker synthesis", mgr.Runner.Settlement.Finish(ctx, sess.ID, false, true, ""))
	finishExecution()
	testutil.FailErr(t, "drain worker synthesis wakes", mgr.Runner.Settlement.Drain(ctx, sess.ID))
	settled, err := st.Get(ctx, sess.ID)
	testutil.FailErr(t, "read completed session", err)
	if settled.Status != api.SessionStatusIdle {
		t.Fatalf("status after acknowledged worker synthesis = %q want idle", settled.Status)
	}
	if loop.Nudges.HasPendingLoopWakes(sess.ID) {
		t.Fatal("completed worker synthesis retained a deferred wake")
	}
}

type unavailableWorkerEvidence struct{ inspector.EvidenceStore }

func (unavailableWorkerEvidence) ReadAll(context.Context, string, string, string, evidence.GateType) ([]evidence.Record, error) {
	return nil, errors.New("fixture evidence store unavailable")
}

func TestWorkerValidationEvidenceUnavailablePreservesDelivery(t *testing.T) {
	mem := store.NewMemory()
	mgr := NewHost(mem, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	mgr.Verification.SetEvidenceStore(unavailableWorkerEvidence{})
	sess, err := mem.Create(t.Context(), api.CreateSessionRequest{}, "")
	testutil.FailErr(t, "create session", err)
	task := &api.WorkerTask{ChildSessionID: sess.ID}
	proof, _ := mgr.Workers.Summaries.Proof(t.Context(), task, nil, "")
	if !proof.Verification.Valid() || proof.Verification.Method != verification.Blocked {
		t.Fatalf("unavailable validation not recorded: %+v", proof)
	}
	proof.ChangedPaths = []string{"material"}
	report := workercompletion.EnrichWorkerCompletionReport(workercompletion.WorkerCompletionReport{LegStatus: "complete"}, proof, "complete")
	if report.LegStatus != "complete" || len(report.EvidenceObligations) != 1 ||
		report.EvidenceObligations[0].Status != workercompletion.EvidenceStatusUnmet ||
		report.EvidenceObligations[0].Reason != proof.Verification.Reason {
		t.Fatalf("delivery or limitation lost: %+v", report)
	}
}
