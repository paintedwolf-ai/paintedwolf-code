package wea_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/documentcore"
	"github.com/lycaon/lycaon/internal/editordoc"
	"github.com/lycaon/lycaon/internal/egressgate"
	"github.com/lycaon/lycaon/internal/egressproxy"
	"github.com/lycaon/lycaon/internal/observability/cmddiag"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testbaseline"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
)

type fixedProjectRoots struct{ p *project.Project }

func (r fixedProjectRoots) Get(context.Context, string) (*project.Project, error) { return r.p, nil }

type stubMergeQueue struct{ task *api.WorkerTask }

func (s *stubMergeQueue) Get(jobID string) (*api.WorkerTask, bool) {
	if s.task == nil || s.task.ID != jobID {
		return nil, false
	}
	cp := *s.task
	return &cp, true
}

func (s *stubMergeQueue) EnsureWorkerBranch(ctx context.Context, jobID string) (*api.WorkerTask, *worker.BranchLease, error) {
	task, ok := s.Get(jobID)
	if !ok {
		return nil, nil, worker.ErrWorkerBranchUnavailable
	}
	lease, err := worker.LeaseExistingBranch(ctx, task)
	if err != nil {
		return nil, nil, err
	}
	return task, lease, nil
}

type stubMergeStore struct {
	commits int
}

func (s *stubMergeStore) BeginMergeApply(context.Context, string) (string, bool, error) {
	return "claim-1", true, nil
}
func (s *stubMergeStore) RenewMergeApply(context.Context, string, string) (bool, error) {
	return true, nil
}
func (s *stubMergeStore) ReleaseMergeApply(context.Context, string, string) error { return nil }
func (s *stubMergeStore) ReclaimExpiredMergeApply(context.Context, string) (string, bool, error) {
	return "claim-1", true, nil
}
func (s *stubMergeStore) ListMergeApplying(context.Context) ([]string, error) { return nil, nil }
func (s *stubMergeStore) SetMergeStatus(context.Context, string, api.WorkerMergeStatus) error {
	return nil
}
func (s *stubMergeStore) SetMergeStatuses(context.Context, []worker.MergeStatusUpdate) error {
	return nil
}
func (s *stubMergeStore) LaterRounds(context.Context, string) ([]string, bool, error) {
	return nil, false, nil
}
func (s *stubMergeStore) CommitPromotion(context.Context, string, string, worker.PromotionCommit) error {
	s.commits++
	return nil
}
func (s *stubMergeStore) ClearWorkerWorkspace(context.Context, string) error { return nil }

type e2eRunOutcome struct {
	PromotedFilesOnDisk   int
	PromotionCommits      int
	EditedFilesOnDisk     int
	RetainedVersionsInDB  int
	EgressHTTPArrivals    int
	EgressRefusals        int
	ToolExecutions        int
	EmittedFindings       int
	ContradictionRuleHits map[cmddiag.RuleID]int
}

// runWiredProductWorkflow executes real production entry points (worker.MergeService.PromoteOverlay,
// editordoc.Service.ApplyAgentEdit + real SQLite sourceledger.History, egressgate.AwaitHTTPRequest +
// real httptest.Server, and toolexecution.Executor.Invoke) without ever calling cmddiag.ObserveWEA
// directly. It independently observes disk bytes, SQLite rows, HTTP arrivals, and tool executions.
func runWiredProductWorkflow(t *testing.T, obs *cmddiag.Observer, injectContradictions bool) e2eRunOutcome {
	t.Helper()
	ctx := context.Background()
	out := e2eRunOutcome{ContradictionRuleHits: map[cmddiag.RuleID]int{}}

	// 1. REAL WORKER PROMOTION (worker.MergeService.PromoteOverlay)
	runPromotion := func(jobID string, touchedPaths []string, fileName, body string) {
		primaryDir := t.TempDir()
		branchDir := filepath.Join(primaryDir, settingsoverlay.DirName(), "overlays", jobID)
		testutil.FailErr(t, "mkdir branch", os.MkdirAll(branchDir, 0o755))
		testutil.FailErr(t, "write branch file", os.WriteFile(filepath.Join(branchDir, fileName), []byte(body), 0o644))
		testbaseline.RecordBranch(t, "root", primaryDir, branchDir)

		scope := api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{fileName}}
		task := &api.WorkerTask{
			ID:                    jobID,
			ParentSessionID:       "11111111-2222-3333-4444-555555555555",
			ProjectID:             testdbseed.DefaultProjectID,
			WorkerName:            "worker-e2e",
			ResultHandle:          "res-" + jobID,
			WorkspacePath:         primaryDir,
			WorkspaceRoot:         branchDir,
			MergeStatus:           api.WorkerMergeStatusPending,
			AgentType:             "implementer",
			Scope:                 &scope,
			TouchedPaths:          touchedPaths,
			WorkspaceBaselinePath: testbaseline.Capture(t, "root", primaryDir),
			Status:                api.WorkerStatusComplete,
		}
		store := &stubMergeStore{}
		queue := &stubMergeQueue{task: task}
		svc := &worker.MergeService{
			Queue:    queue,
			Branches: queue,
			Store:    store,
			DataDir:  t.TempDir(),
		}
		res, err := svc.PromoteOverlay(ctx, task.ParentSessionID, jobID, api.PromoteOverlayInput{})
		testutil.FailErr(t, "PromoteOverlay "+jobID, err)
		if res.Status != api.WorkerMergeStatusMerged {
			t.Fatalf("PromoteOverlay %s status=%q want merged", jobID, res.Status)
		}
		gotBytes, err := os.ReadFile(filepath.Join(primaryDir, fileName))
		testutil.FailErr(t, "independent disk read after promotion", err)
		if string(gotBytes) != body {
			t.Fatalf("promoted disk bytes mismatch: got %q want %q", string(gotBytes), body)
		}
		out.PromotedFilesOnDisk++
		out.PromotionCommits += store.commits
	}

	// Healthy promotion: TouchedPaths matches landed file.
	runPromotion("job-healthy-1", []string{"landed.go"}, "landed.go", "package landed\n")
	if injectContradictions {
		// Contradiction #1 (RuleWEALandedPathMismatch): landed.go absent from committed TouchedPaths.
		runPromotion("job-unlisted-1", []string{"other.go"}, "landed.go", "package landed\n")
	}

	// 2. REAL EDITORDOC + REAL SQLITE SOURCELEDGER (editordoc.Service.ApplyAgentEdit)
	sqlDB := testdbfixture.Open(t, "tb11_editordoc.db")
	editRoot := t.TempDir()
	projectID, rootID := testdbseed.DefaultProjectID, uuid.NewString()
	testdbseed.InsertProjectRootWithID(t, sqlDB, projectID, rootID, editRoot)
	testutil.FailErr(t, "seed app.go", os.WriteFile(filepath.Join(editRoot, "app.go"), []byte("line1\nline2\n"), 0o644))
	proj := &project.Project{ID: projectID, Roots: []project.Root{{ID: rootID, ProjectID: projectID, Path: editRoot, IsPrimary: true}}}
	ledger := sourceledger.New(sqlDB, t.TempDir(), sourceledger.Timing{})
	editSvc := editordoc.New(editordoc.NewStore(sqlDB), ledger, ledger.History, fixedProjectRoots{p: proj})
	t.Cleanup(func() { _ = editSvc.Close(context.Background()) })

	doc, ok, err := editSvc.OpenText(ctx, projectID, sourcebranch.Trunk, rootID, "app.go")
	testutil.FailErr(t, "OpenText", err)
	if !ok || doc == nil {
		t.Fatal("expected OpenText to open app.go")
	}

	// Healthy edit: whole-document read recorded in owning a.agentReads cache.
	editSvc.RememberAgentRead(projectID, "11111111-2222-3333-4444-555555555555", doc.ID, doc.Revision)
	opHealthy := uuid.NewString()
	resEdit, err := editSvc.ApplyAgentEdit(ctx, editordoc.AgentEdit{
		ProjectID: projectID, DocumentID: doc.ID, ExpectedRevision: doc.Revision,
		Content: "line1\nline2-edited\n", OperationID: opHealthy,
		SessionID: "11111111-2222-3333-4444-555555555555", Turn: 1, ToolCallID: "call-edit-1", ToolName: "edit",
	})
	testutil.FailErr(t, "ApplyAgentEdit healthy", err)
	if !resEdit.Saved {
		t.Fatal("expected healthy ApplyAgentEdit to save to disk")
	}
	diskAfter, err := os.ReadFile(filepath.Join(editRoot, "app.go"))
	testutil.FailErr(t, "independent disk read after edit", err)
	if string(diskAfter) != "line1\nline2-edited\n" {
		t.Fatalf("unexpected disk content: %q", string(diskAfter))
	}
	out.EditedFilesOnDisk++

	if injectContradictions {
		// Contradiction #2 (RuleWEAEditStaleReadBasis): partial span read only (not whole).
		testutil.FailErr(t, "seed partial.go", os.WriteFile(filepath.Join(editRoot, "partial.go"), []byte("a\nb\nc\n"), 0o644))
		docPartial, ok, err := editSvc.OpenText(ctx, projectID, sourcebranch.Trunk, rootID, "partial.go")
		testutil.FailErr(t, "OpenText partial.go", err)
		if !ok {
			t.Fatal("expected partial.go open")
		}
		editSvc.RememberAgentReadSpans(projectID, "11111111-2222-3333-4444-555555555555", docPartial.ID, docPartial.Revision, []documentcore.LineSpan{{StartLine: 1, EndLine: 1}})
		_, err = editSvc.ApplyAgentEdit(ctx, editordoc.AgentEdit{
			ProjectID: projectID, DocumentID: docPartial.ID, ExpectedRevision: docPartial.Revision,
			Content: "a\nb-edited\nc\n", OperationID: uuid.NewString(),
			SessionID: "11111111-2222-3333-4444-555555555555", Turn: 2, ToolCallID: "call-edit-partial", ToolName: "edit",
		})
		testutil.FailErr(t, "ApplyAgentEdit partial", err)
		out.EditedFilesOnDisk++

		// Contradiction #3 (RuleWEAEditWrongAuthor): tamper retained source_versions row in SQLite
		// before the deferred editordoc QueryFileVersions reader inspects it.
		testutil.FailErr(t, "seed author.go", os.WriteFile(filepath.Join(editRoot, "author.go"), []byte("v1\n"), 0o644))
		docAuthor, ok, err := editSvc.OpenText(ctx, projectID, sourcebranch.Trunk, rootID, "author.go")
		testutil.FailErr(t, "OpenText author.go", err)
		if !ok {
			t.Fatal("expected author.go open")
		}
		editSvc.RememberAgentRead(projectID, "11111111-2222-3333-4444-555555555555", docAuthor.ID, docAuthor.Revision)
		gate := make(chan struct{})
		entered := make(chan struct{})
		if obs != nil && cmddiag.Enabled() {
			obs.SubmitTask(cmddiag.Task{
				ProgramID: "barrier-author-tamper",
				Fetch: func(taskCtx context.Context) (cmddiag.Observation, error) {
					close(entered)
					select {
					case <-gate:
					case <-taskCtx.Done():
					}
					return cmddiag.WrapWEAObservation(cmddiag.WEAObservation{Settled: true}), nil
				},
			})
			<-entered
		}
		opTamper := uuid.NewString()
		_, err = editSvc.ApplyAgentEdit(ctx, editordoc.AgentEdit{
			ProjectID: projectID, DocumentID: docAuthor.ID, ExpectedRevision: docAuthor.Revision,
			Content: "v2\n", OperationID: opTamper,
			SessionID: "11111111-2222-3333-4444-555555555555", Turn: 3, ToolCallID: "call-edit-author", ToolName: "edit",
		})
		testutil.FailErr(t, "ApplyAgentEdit author", err)
		_, err = sqlDB.ExecContext(ctx, `UPDATE source_operations SET origin='external', tool_call_id='call-tampered' WHERE id IN (SELECT operation_id FROM source_versions WHERE file_id=?)`, docAuthor.FileID)
		testutil.FailErr(t, "tamper persisted source_operations author", err)
		close(gate)
		out.EditedFilesOnDisk++
	}

	// Independently count retained versions persisted in SQLite for app.go.
	versionsPage, err := ledger.History.QueryFileVersions(ctx, projectID, doc.FileID, 32, 0)
	testutil.FailErr(t, "QueryFileVersions independent count", err)
	out.RetainedVersionsInDB = len(versionsPage.Versions)

	// 3. REAL EGRESSGATE + REAL HTTP FIXTURE SERVER (egressgate.AwaitHTTPRequest)
	var httpArrivals atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpArrivals.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	targetURL, err := url.Parse(srv.URL + "/ping")
	testutil.FailErr(t, "parse fixture server URL", err)
	portNum, err := strconv.Atoi(targetURL.Port())
	testutil.FailErr(t, "parse fixture server port", err)

	prevPosture := confine.EffectivePosture(confine.EgressCommand{})
	defer func() {
		confine.SetEgressPosture(prevPosture)
		confine.SetEgressResolver(nil)
	}()

	// Healthy allowed egress + real HTTP arrival.
	confine.SetEgressPosture(confine.PostureAsk)
	confine.SetEgressResolver(func(context.Context, confine.EgressCommand, egressproxy.Endpoint, *confine.EgressDetectionCitation) bool {
		return true
	})
	egressCtx := egressgate.WithAttribution(ctx, confine.EgressCommand{
		OwnedLoopbackPorts: []uint16{uint16(portNum)},
		SessionID:          "11111111-2222-3333-4444-555555555555",
		ProjectID:          projectID,
		ActionID:           "act-egress-allow",
		ToolCallID:         "call-egress-allow",
	})
	testutil.FailErr(t, "AwaitHTTPRequest allow", egressgate.AwaitHTTPRequest(egressCtx, targetURL, http.MethodGet))
	confine.ForgetEgressAction("11111111-2222-3333-4444-555555555555", "act-egress-allow")
	resp, err := http.Get(targetURL.String())
	testutil.FailErr(t, "HTTP GET fixture", err)
	_ = resp.Body.Close()

	// Expected refusal: resolver denies host and must not dial fixture server.
	confine.SetEgressResolver(func(context.Context, confine.EgressCommand, egressproxy.Endpoint, *confine.EgressDetectionCitation) bool {
		return false
	})
	denyCtx := egressgate.WithAttribution(ctx, confine.EgressCommand{
		SessionID:  "11111111-2222-3333-4444-555555555555",
		ProjectID:  projectID,
		ActionID:   "act-egress-deny",
		ToolCallID: "call-egress-deny",
	})
	if err := egressgate.AwaitHTTPRequest(denyCtx, targetURL, http.MethodGet); err == nil {
		t.Fatal("expected AwaitHTTPRequest to refuse when resolver denies")
	} else {
		out.EgressRefusals++
	}
	confine.ForgetEgressAction("11111111-2222-3333-4444-555555555555", "act-egress-deny")
	out.EgressHTTPArrivals = int(httpArrivals.Load())

	// 4. REAL TOOLEXECUTION.EXECUTOR.INVOKE
	var toolRuns atomic.Int32
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register verify", reg.Register("verify", func(context.Context, map[string]any, tools.ToolContext) (string, error) {
		toolRuns.Add(1)
		return "probe-ok", nil
	}))
	boundary := sandbox.NewBoundary(sandbox.Config{}, []sandbox.ToolProfile{{
		ID:    "default",
		Tools: map[string]bool{"verify": true},
	}})
	policy := toolprofiles.NewProfilePolicyEngine(boundary)
	exec := toolexecution.NewExecutor(policy, reg, "default")
	toolOut, err := exec.Invoke(ctx, "verify", map[string]any{"command": "true"}, tools.ToolContext{
		Identity: tools.InvocationIdentity{
			SessionID:  "11111111-2222-3333-4444-555555555555",
			ProjectID:  projectID,
			ToolCallID: "call-probe-1",
		},
	})
	testutil.FailErr(t, "Executor.Invoke", err)
	if toolOut != "probe-ok" {
		t.Fatalf("Executor.Invoke output=%q want probe-ok", toolOut)
	}
	out.ToolExecutions = int(toolRuns.Load())

	return out
}

func TestTB11WiredEndToEndParityAndContradictions(t *testing.T) {
	fixed := time.Date(2026, 11, 27, 14, 0, 0, 0, time.UTC)

	// 1. Healthy workflow: DISABLED vs ENABLED parity (0 contradictions on both).
	obsOff := cmddiag.NewObserver(cmddiag.Config{Enabled: false})
	restoreOff := cmddiag.SetObserver(obsOff)
	healthyOff := runWiredProductWorkflow(t, obsOff, false)
	restoreOff()
	obsOff.Close()

	var mu sync.Mutex
	var healthyFindings []cmddiag.Finding
	obsHealthyOn := cmddiag.NewObserver(cmddiag.Config{
		Enabled:       true,
		QueueCapacity: 64,
		Now:           func() time.Time { return fixed },
		Sink: func(f cmddiag.Finding) {
			mu.Lock()
			healthyFindings = append(healthyFindings, f)
			mu.Unlock()
		},
	})
	restoreHealthyOn := cmddiag.SetObserver(obsHealthyOn)
	healthyOn := runWiredProductWorkflow(t, obsHealthyOn, false)
	restoreHealthyOn()
	obsHealthyOn.Close()

	// Assert strict functional parity between DISABLED and ENABLED runs.
	if healthyOff.PromotedFilesOnDisk != healthyOn.PromotedFilesOnDisk ||
		healthyOff.PromotionCommits != healthyOn.PromotionCommits ||
		healthyOff.EditedFilesOnDisk != healthyOn.EditedFilesOnDisk ||
		healthyOff.RetainedVersionsInDB != healthyOn.RetainedVersionsInDB ||
		healthyOff.EgressHTTPArrivals != healthyOn.EgressHTTPArrivals ||
		healthyOff.EgressRefusals != healthyOn.EgressRefusals ||
		healthyOff.ToolExecutions != healthyOn.ToolExecutions {
		t.Fatalf("healthy enabled/disabled parity mismatch:\noff=%+v\non=%+v", healthyOff, healthyOn)
	}
	if obsOff.Stats().Emitted != 0 {
		t.Fatalf("disabled observer emitted %d findings, want 0", obsOff.Stats().Emitted)
	}
	mu.Lock()
	for _, f := range healthyFindings {
		if f.Disposition == cmddiag.DispositionContradiction {
			t.Fatalf("healthy wired product workflow emitted unexpected contradiction: %+v", f)
		}
	}
	mu.Unlock()

	// 2. Contradiction workflow through real wired production entry points (DISABLED vs ENABLED).
	obsInjOff := cmddiag.NewObserver(cmddiag.Config{Enabled: false})
	restoreInjOff := cmddiag.SetObserver(obsInjOff)
	injOff := runWiredProductWorkflow(t, obsInjOff, true)
	restoreInjOff()
	obsInjOff.Close()

	var injFindings []cmddiag.Finding
	obsInjOn := cmddiag.NewObserver(cmddiag.Config{
		Enabled:       true,
		QueueCapacity: 64,
		Now:           func() time.Time { return fixed },
		Sink: func(f cmddiag.Finding) {
			mu.Lock()
			injFindings = append(injFindings, f)
			mu.Unlock()
		},
	})
	restoreInjOn := cmddiag.SetObserver(obsInjOn)
	injOn := runWiredProductWorkflow(t, obsInjOn, true)
	restoreInjOn()
	obsInjOn.Close()

	mu.Lock()
	defer mu.Unlock()
	injOn.EmittedFindings = len(injFindings)
	for _, f := range injFindings {
		if f.Disposition == cmddiag.DispositionContradiction {
			injOn.ContradictionRuleHits[f.RuleID]++
		}
	}

	var b strings.Builder
	b.WriteString("\nTB11 Wired Production E2E Parity Receipt (zero direct cmddiag.ObserveWEA calls):\n")
	b.WriteString(fmt.Sprintf("  %-26s | %-10s | %-10s\n", "Metric (Healthy Run)", "Disabled", "Enabled"))
	b.WriteString(fmt.Sprintf("  %-26s-+-%-10s-+-%-10s\n", "--------------------------", "----------", "----------"))
	b.WriteString(fmt.Sprintf("  %-26s | %-10d | %-10d\n", "PromotedFilesOnDisk", healthyOff.PromotedFilesOnDisk, healthyOn.PromotedFilesOnDisk))
	b.WriteString(fmt.Sprintf("  %-26s | %-10d | %-10d\n", "PromotionCommits", healthyOff.PromotionCommits, healthyOn.PromotionCommits))
	b.WriteString(fmt.Sprintf("  %-26s | %-10d | %-10d\n", "EditedFilesOnDisk", healthyOff.EditedFilesOnDisk, healthyOn.EditedFilesOnDisk))
	b.WriteString(fmt.Sprintf("  %-26s | %-10d | %-10d\n", "RetainedVersionsInDB", healthyOff.RetainedVersionsInDB, healthyOn.RetainedVersionsInDB))
	b.WriteString(fmt.Sprintf("  %-26s | %-10d | %-10d\n", "EgressHTTPArrivals", healthyOff.EgressHTTPArrivals, healthyOn.EgressHTTPArrivals))
	b.WriteString(fmt.Sprintf("  %-26s | %-10d | %-10d\n", "EgressRefusals", healthyOff.EgressRefusals, healthyOn.EgressRefusals))
	b.WriteString(fmt.Sprintf("  %-26s | %-10d | %-10d\n", "ToolExecutions", healthyOff.ToolExecutions, healthyOn.ToolExecutions))
	b.WriteString(fmt.Sprintf("  %-26s | %-10d | %-10d\n", "HealthyContradictions", 0, 0))
	b.WriteString(fmt.Sprintf("  %-26s | %-10d | %-10d\n", "InjectedContradictionsE2E", len(injOff.ContradictionRuleHits), len(injOn.ContradictionRuleHits)))
	b.WriteString("  Component-boundary contradictions (unexported broker/approval bypass hooks):\n")
	b.WriteString("    - RuleWEAUnauthorizedEffect: internal/toolexecution/wea_diagnostics_test.go\n")
	b.WriteString("    - RuleWEAUnrecordedOrRefusedEgress: internal/confine/wea_diagnostics_test.go\n")
	t.Log(b.String())

	// Assert identical functional outcomes on injected run too.
	if injOff.PromotedFilesOnDisk != injOn.PromotedFilesOnDisk ||
		injOff.EditedFilesOnDisk != injOn.EditedFilesOnDisk ||
		injOff.RetainedVersionsInDB != injOn.RetainedVersionsInDB ||
		injOff.EgressHTTPArrivals != injOn.EgressHTTPArrivals ||
		injOff.ToolExecutions != injOn.ToolExecutions {
		t.Fatalf("injected enabled/disabled parity mismatch:\noff=%+v\non=%+v", injOff, injOn)
	}
	if obsInjOff.Stats().Emitted != 0 {
		t.Fatalf("disabled injected run emitted %d findings, want 0", obsInjOff.Stats().Emitted)
	}

	// Assert all 3 end-to-end injectable contradictions fired through real production owners.
	for _, want := range []cmddiag.RuleID{
		cmddiag.RuleWEALandedPathMismatch,
		cmddiag.RuleWEAEditStaleReadBasis,
		cmddiag.RuleWEAEditWrongAuthor,
	} {
		if injOn.ContradictionRuleHits[want] != 1 {
			t.Fatalf("expected wired production owner to emit %q once, got hits=%+v (all=%+v)", want, injOn.ContradictionRuleHits, injFindings)
		}
	}
}
