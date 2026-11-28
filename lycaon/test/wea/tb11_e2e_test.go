package wea_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/documentcore"
	"github.com/lycaon/lycaon/internal/editordoc"
	_ "github.com/lycaon/lycaon/internal/egressgate"
	"github.com/lycaon/lycaon/internal/egressproxy"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/lineage"
	"github.com/lycaon/lycaon/internal/observability/cmddiag"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testbaseline"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolapproval"
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

type stubMergeStore struct{ commits int }

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

// tb11ApprovalGate requires human tool_approval on every evaluated action so the
// real toolexecution.Executor -> hitl.Checkpoints -> ResolveApprovalOption ->
// post-execution lifecycle is exercised end-to-end.
type tb11ApprovalGate struct{}

func (tb11ApprovalGate) Evaluate(context.Context, hitl.ProposedAction) (*hitl.ApprovalResult, error) {
	return &hitl.ApprovalResult{
		Decision: &gate.Decision{
			Primary: api.GateAuthorityMisuse,
			Cited:   []gate.Fact{{Gate: api.GateAuthorityMisuse, Key: "detection.rule", Value: "tb11 authority check", Source: "detection_pack"}},
		},
		DetectionCitation: &hitl.DetectionMatch{
			PackID: "tb11-pack", RuleID: "tb11-rule", RuleTitle: "tb11 authority check", Level: "critical",
			External: true, Unrecoverable: true, Tagged: true,
		},
	}, nil
}
func (tb11ApprovalGate) GrantOffers(hitl.ProposedAction, *hitl.ApprovalResult) []hitl.ApprovalGrantOffer {
	return nil
}
func (tb11ApprovalGate) AbsorbedGrantOffers(hitl.ProposedAction, *hitl.ApprovalResult) []hitl.ApprovalGrantOffer {
	return nil
}
func (tb11ApprovalGate) ApplyGrant(hitl.ApprovalGrant) (bool, error)         { return false, nil }
func (tb11ApprovalGate) GrantCovers(hitl.ProposedAction) bool                { return false }
func (tb11ApprovalGate) HostResourceLeaseCovers(hitl.ProposedAction) bool    { return false }
func (tb11ApprovalGate) RevokeGrant(string) (bool, error)                    { return false, nil }
func (tb11ApprovalGate) RevokeGrantInstalledBy(string, string) (bool, error) { return false, nil }
func (tb11ApprovalGate) ListGrants(string) []hitl.ApprovalGrant              { return nil }
func (tb11ApprovalGate) SecretFingerprintsCovered(string, string, string, string, []string) bool {
	return false
}
func (tb11ApprovalGate) SecretRedactionStanding(string, []string) bool { return false }
func (tb11ApprovalGate) PutAskQuiet(q hitl.AskQuiet, _ int) (hitl.AskQuiet, bool) {
	return q, false
}
func (tb11ApprovalGate) AskQuietLive(string, string) (hitl.AskQuiet, bool) {
	return hitl.AskQuiet{}, false
}
func (tb11ApprovalGate) NoteAskQuietSuppressed(string, string)         {}
func (tb11ApprovalGate) ListAskQuiets(string) []hitl.AskQuiet          { return nil }
func (tb11ApprovalGate) RevokeAskQuiet(string) bool                    { return false }
func (tb11ApprovalGate) RevokeAskQuietInstalledBy(string, string) bool { return false }
func (tb11ApprovalGate) ForgetSession(string)                          {}

type tb11ApprovalExplainer struct{}

func (tb11ApprovalExplainer) ExplainApproval(hitl.ProposedAction) toolapproval.ApprovalExplanation {
	return toolapproval.ApprovalExplanation{
		What:      "Read project file app.go",
		Who:       "Agent",
		IfWrong:   "Reads file bytes from the workspace",
		AllowLine: "Allow reading app.go once",
	}
}

type tb11NoopAuthzRecorder struct{}

func (tb11NoopAuthzRecorder) AppendToolDenied(context.Context, authzledger.ToolDeniedRecord) {}
func (tb11NoopAuthzRecorder) AppendCapabilityRecord(context.Context, authzledger.CapabilityRecord) error {
	return nil
}
func (tb11NoopAuthzRecorder) AppendDirectIPLifecycle(context.Context, authzledger.DirectIPLifecycleRecord) {
}
func (tb11NoopAuthzRecorder) AppendMediatedEndpoint(context.Context, authzledger.MediatedEndpointRecord) {
}
func (tb11NoopAuthzRecorder) AppendApprovalGateTx(context.Context, *sql.Tx, authzledger.ApprovalDecisionRecord) error {
	return nil
}
func (tb11NoopAuthzRecorder) AppendCapabilityGateTx(context.Context, *sql.Tx, authzledger.CapabilityRecord) error {
	return nil
}
func (tb11NoopAuthzRecorder) AppendHumanGateTx(context.Context, *sql.Tx, authzledger.HumanGateRecord) error {
	return nil
}

type tb11NoopInstaller struct{}

func (tb11NoopInstaller) InstallApprovalOption(context.Context, string, hitl.ApprovalOption) (func(), error) {
	return func() {}, nil
}

type tb11CheckpointEvents struct{ pending chan string }

func (e tb11CheckpointEvents) PublishCheckpoint(_ context.Context, _, _ string, ev api.CheckpointEvent) {
	if ev.Status == "pending" && ev.ID != "" {
		select {
		case e.pending <- ev.ID:
		default:
		}
	}
}
func (tb11CheckpointEvents) PublishAttention(context.Context) {}

func driveBrokerCurl(t *testing.T, ctx context.Context, c *confine.Confinement, brokerAddr, targetURL string) string {
	t.Helper()
	curlPath, err := exec.LookPath("curl")
	if err != nil {
		t.Skip("curl is required to drive a confined process through the confine egress broker")
	}
	cmd, cleanup, err := confine.Command(ctx, os.Args[0], curlPath, []string{
		"-s", "-o", "/dev/null", "-m", "5", "-w", "%{http_code}", "-x", "http://" + brokerAddr, targetURL,
	}, *c)
	testutil.FailErr(t, "confine.Command curl", err)
	defer cleanup()
	out, err := cmd.Output()
	if err != nil && len(out) == 0 {
		t.Fatalf("confined curl failed: %v", err)
	}
	return strings.TrimSpace(string(out))
}

type e2eRunOutcome struct {
	PromotedFilesOnDisk   int
	PromotionCommits      int
	EditedFilesOnDisk     int
	RetainedVersionsInDB  int
	BrokerAllowedReceipts int
	BrokerDeniedReceipts  int
	BrokerHTTPArrivals    int
	ApprovalResolutions   int
	AuthorizedToolRuns    int
	EmittedFindings       int
	ContradictionRuleHits map[cmddiag.RuleID]int
}

// runWiredProductWorkflow executes real production entry points without ever
// calling cmddiag.ObserveWEA directly:
//  1. worker.MergeService.PromoteOverlay (real disk writes + commits)
//  2. editordoc.Service.ApplyAgentEdit + real SQLite sourceledger.History
//  3. confine.StartEgressBroker + BindAction + real descendant curl through broker
//     (real broker-owned positive AND denial receipts + real HTTP arrivals)
//  4. toolexecution.Executor.Invoke("read") + real SQLite hitl.Checkpoints +
//     hitl.ApprovalAuthority.ResolveApprovalOption("approve_current_action")
func runWiredProductWorkflow(t *testing.T, obs *cmddiag.Observer, injectContradictions bool) e2eRunOutcome {
	t.Helper()
	ctx := context.Background()
	out := e2eRunOutcome{ContradictionRuleHits: map[cmddiag.RuleID]int{}}
	const sessionID = "11111111-2222-3333-4444-555555555555"

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
			ParentSessionID:       sessionID,
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

	runPromotion("job-healthy-1", []string{"landed.go"}, "landed.go", "package landed\n")
	if injectContradictions {
		// Wired contradiction #1 (RuleWEALandedPathMismatch): landed.go absent from TouchedPaths.
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

	editSvc.RememberAgentRead(projectID, sessionID, doc.ID, doc.Revision)
	resEdit, err := editSvc.ApplyAgentEdit(ctx, editordoc.AgentEdit{
		ProjectID: projectID, DocumentID: doc.ID, ExpectedRevision: doc.Revision,
		Content: "line1\nline2-edited\n", OperationID: uuid.NewString(),
		SessionID: sessionID, Turn: 1, ToolCallID: "call-edit-1", ToolName: "edit",
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
		// Wired contradiction #2 (RuleWEAEditStaleReadBasis): partial span read only.
		testutil.FailErr(t, "seed partial.go", os.WriteFile(filepath.Join(editRoot, "partial.go"), []byte("a\nb\nc\n"), 0o644))
		docPartial, ok, err := editSvc.OpenText(ctx, projectID, sourcebranch.Trunk, rootID, "partial.go")
		testutil.FailErr(t, "OpenText partial.go", err)
		if !ok {
			t.Fatal("expected partial.go open")
		}
		editSvc.RememberAgentReadSpans(projectID, sessionID, docPartial.ID, docPartial.Revision, []documentcore.LineSpan{{StartLine: 1, EndLine: 1}})
		_, err = editSvc.ApplyAgentEdit(ctx, editordoc.AgentEdit{
			ProjectID: projectID, DocumentID: docPartial.ID, ExpectedRevision: docPartial.Revision,
			Content: "a\nb-edited\nc\n", OperationID: uuid.NewString(),
			SessionID: sessionID, Turn: 2, ToolCallID: "call-edit-partial", ToolName: "edit",
		})
		testutil.FailErr(t, "ApplyAgentEdit partial", err)
		out.EditedFilesOnDisk++

		// Wired contradiction #3 (RuleWEAEditWrongAuthor): tamper persisted source_operations
		// author before editordoc's deferred QueryFileVersions reader inspects it.
		testutil.FailErr(t, "seed author.go", os.WriteFile(filepath.Join(editRoot, "author.go"), []byte("v1\n"), 0o644))
		docAuthor, ok, err := editSvc.OpenText(ctx, projectID, sourcebranch.Trunk, rootID, "author.go")
		testutil.FailErr(t, "OpenText author.go", err)
		if !ok {
			t.Fatal("expected author.go open")
		}
		editSvc.RememberAgentRead(projectID, sessionID, docAuthor.ID, docAuthor.Revision)
		gateCh := make(chan struct{})
		entered := make(chan struct{})
		if obs != nil && cmddiag.Enabled() {
			obs.SubmitTask(cmddiag.Task{
				ProgramID: "barrier-author-tamper",
				Fetch: func(taskCtx context.Context) (cmddiag.Observation, error) {
					close(entered)
					select {
					case <-gateCh:
					case <-taskCtx.Done():
					}
					return cmddiag.WrapWEAObservation(cmddiag.WEAObservation{Settled: true}), nil
				},
			})
			<-entered
		}
		_, err = editSvc.ApplyAgentEdit(ctx, editordoc.AgentEdit{
			ProjectID: projectID, DocumentID: docAuthor.ID, ExpectedRevision: docAuthor.Revision,
			Content: "v2\n", OperationID: uuid.NewString(),
			SessionID: sessionID, Turn: 3, ToolCallID: "call-edit-author", ToolName: "edit",
		})
		testutil.FailErr(t, "ApplyAgentEdit author", err)
		_, err = sqlDB.ExecContext(ctx, `UPDATE source_operations SET origin='external', tool_call_id='call-tampered' WHERE id IN (SELECT operation_id FROM source_versions WHERE file_id=?)`, docAuthor.FileID)
		testutil.FailErr(t, "tamper persisted source_operations author", err)
		close(gateCh)
		out.EditedFilesOnDisk++
	}

	versionsPage, err := ledger.History.QueryFileVersions(ctx, projectID, doc.FileID, 32, 0)
	testutil.FailErr(t, "QueryFileVersions independent count", err)
	out.RetainedVersionsInDB = len(versionsPage.Versions)

	// 3. REAL CONFINE EGRESS BROKER (positive + denial receipts with shared action/destination identities)
	testutil.RequireLineage(t, lineage.Supported())
	addrs, err := confine.StartEgressBroker(t.TempDir(), net.Listen)
	testutil.FailErr(t, "start confine egress broker", err)
	defer func() { _ = confine.StopEgressBroker() }()

	var httpArrivals atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpArrivals.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	prevPosture := confine.EffectivePosture(confine.EgressCommand{})
	defer func() {
		confine.SetEgressPosture(prevPosture)
		confine.SetEgressResolver(nil)
	}()

	// 3a. Broker-owned positive receipt: descendant dials fixture through real broker while allowed.
	confine.SetEgressPosture(confine.PostureAsk)
	confine.SetEgressResolver(func(context.Context, confine.EgressCommand, egressproxy.Endpoint, *confine.EgressDetectionCitation) bool {
		return true
	})
	allowConf := &confine.Confinement{Network: confine.NetworkProxyOnly, ProjectID: projectID, LoopbackConnect: true, HostExecution: true}
	allowCmd := confine.EgressCommand{SessionID: sessionID, ProjectID: projectID, ActionID: "act-broker-allow", ToolCallID: "call-broker-allow"}
	allowLease, err := confine.BindAction(allowConf, allowCmd)
	testutil.FailErr(t, "BindAction allow", err)
	statusAllow := driveBrokerCurl(t, ctx, allowConf, addrs.HTTP, srv.URL+"/ping")
	allowHosts := allowLease.Close(ctx)
	confine.ForgetEgressAction(sessionID, allowCmd.ActionID)
	if statusAllow != "200" {
		t.Fatalf("broker allow HTTP status=%q want 200", statusAllow)
	}
	for _, h := range allowHosts {
		if h.Allowed && (h.Outcome == "connected" || h.Outcome == "response_received") {
			out.BrokerAllowedReceipts++
		}
	}
	if out.BrokerAllowedReceipts == 0 {
		t.Fatalf("expected broker-owned connected receipt, got %+v", allowHosts)
	}

	// 3b. Broker-owned denial receipt: descendant dials fixture through real broker while denied.
	confine.SetEgressResolver(func(context.Context, confine.EgressCommand, egressproxy.Endpoint, *confine.EgressDetectionCitation) bool {
		return false
	})
	denyConf := &confine.Confinement{Network: confine.NetworkProxyOnly, ProjectID: projectID, LoopbackConnect: true, HostExecution: true}
	denyCmd := confine.EgressCommand{SessionID: sessionID, ProjectID: projectID, ActionID: "act-broker-deny", ToolCallID: "call-broker-deny"}
	denyLease, err := confine.BindAction(denyConf, denyCmd)
	testutil.FailErr(t, "BindAction deny", err)
	statusDeny := driveBrokerCurl(t, ctx, denyConf, addrs.HTTP, srv.URL+"/denied")
	denyHosts := denyLease.Close(ctx)
	confine.ForgetEgressAction(sessionID, denyCmd.ActionID)
	if statusDeny == "200" {
		t.Fatalf("broker deny unexpectedly returned HTTP 200")
	}
	for _, h := range denyHosts {
		if !h.Allowed {
			out.BrokerDeniedReceipts++
		}
	}
	if out.BrokerDeniedReceipts == 0 {
		t.Fatalf("expected broker-owned denial receipt, got %+v", denyHosts)
	}
	out.BrokerHTTPArrivals = int(httpArrivals.Load())

	// 4. REAL TOOLEXECUTION.EXECUTOR + REAL SQLITE HITL.CHECKPOINTS APPROVAL LIFECYCLE
	// Authority effect exercised: tool_approval checkpoint on "read" ->
	// hitl.ApprovalAuthority.ResolveApprovalOption("approve_current_action") ->
	// toolexecution.Executor.Invoke("read") -> post-execution authority effect joined
	// against hitl's ApprovalPlan by ActionDigest + CheckpointID.
	testdbseed.InsertSession(t, sqlDB, sessionID, projectID)
	ownerCtx := testdbseed.OwnerCaller(t, ctx, sqlDB)
	evPub := tb11CheckpointEvents{pending: make(chan string, 4)}
	cpMgr := hitl.NewCheckpoints(hitl.NewSQLStore(sqlDB), evPub, tb11NoopAuthzRecorder{})
	cpMgr.Authority.SetApprovalAuthorityInstaller(tb11NoopInstaller{})

	var toolRuns atomic.Int32
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register read", reg.Register("read", func(context.Context, map[string]any, tools.ToolContext) (string, error) {
		toolRuns.Add(1)
		return "read-ok", nil
	}))
	boundary := sandbox.NewBoundary(sandbox.Config{}, []sandbox.ToolProfile{{
		ID:    "implement",
		Tools: map[string]bool{"read": true},
	}})
	gateImpl := tb11ApprovalGate{}
	policy := toolexecution.NewApprovalPolicyEngine(toolprofiles.NewProfilePolicyEngine(boundary), gateImpl)
	execSvc := toolexecution.NewExecutor(policy, reg, "implement")
	execSvc.Approvals.SetApprovalExplainer(tb11ApprovalExplainer{})
	execSvc.Approvals.SetCheckpointManager(ctx, cpMgr, gateImpl)

	type invokeRes struct {
		out string
		err error
	}
	invokeCh := make(chan invokeRes, 1)
	go func() {
		res, invErr := execSvc.Invoke(ctx, "read", map[string]any{"path": "app.go"}, tools.ToolContext{
			Source: tools.InvocationSource{
				Roots:        []projectroot.RootRef{{ID: rootID, Label: "root", Path: editRoot, IsPrimary: true}},
				ActiveRootID: rootID,
			},
			Identity: tools.InvocationIdentity{
				SessionID:  sessionID,
				ProjectID:  projectID,
				ToolCallID: "call-read-auth-1",
				Agent:      "implement",
			},
		})
		invokeCh <- invokeRes{res, invErr}
	}()

	select {
	case cpID := <-evPub.pending:
		resolved, resolveErr := cpMgr.Authority.ResolveApprovalOption(ownerCtx, sessionID, cpID, "approve_current_action")
		testutil.FailErr(t, "ResolveApprovalOption approve_current_action", resolveErr)
		if resolved == nil || resolved.Status != hitl.DecisionStatusApproved {
			t.Fatalf("unexpected resolved checkpoint: %+v", resolved)
		}
		out.ApprovalResolutions++
	case early := <-invokeCh:
		t.Fatalf("Invoke returned before tool_approval checkpoint: out=%q err=%v", early.out, early.err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for real hitl tool_approval checkpoint")
	}

	invRes := <-invokeCh
	testutil.FailErr(t, "Executor.Invoke read after real hitl approval", invRes.err)
	if invRes.out != "read-ok" {
		t.Fatalf("Executor.Invoke output=%q want read-ok", invRes.out)
	}
	out.AuthorizedToolRuns = int(toolRuns.Load())

	return out
}

func TestTB11WiredEndToEndParityAndContradictions(t *testing.T) {
	fixed := time.Date(2026, 11, 27, 14, 0, 0, 0, time.UTC)

	// 1. Healthy wired workflow: DISABLED vs ENABLED parity (0 contradictions on both).
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

	if healthyOff.PromotedFilesOnDisk != healthyOn.PromotedFilesOnDisk ||
		healthyOff.PromotionCommits != healthyOn.PromotionCommits ||
		healthyOff.EditedFilesOnDisk != healthyOn.EditedFilesOnDisk ||
		healthyOff.RetainedVersionsInDB != healthyOn.RetainedVersionsInDB ||
		healthyOff.BrokerAllowedReceipts != healthyOn.BrokerAllowedReceipts ||
		healthyOff.BrokerDeniedReceipts != healthyOn.BrokerDeniedReceipts ||
		healthyOff.BrokerHTTPArrivals != healthyOn.BrokerHTTPArrivals ||
		healthyOff.ApprovalResolutions != healthyOn.ApprovalResolutions ||
		healthyOff.AuthorizedToolRuns != healthyOn.AuthorizedToolRuns {
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

	// 2. Wired contradiction injection (worker landing + edit read-basis + edit retained author): DISABLED vs ENABLED.
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

	// 3. Safe synthetic checker contradictions with independent source linkage for the two
	//    unbypassable authority/broker invariants (RuleWEAUnauthorizedEffect + RuleWEAUnrecordedOrRefusedEgress).
	synthFindings := cmddiag.EvaluateWEA(cmddiag.WEAObservation{
		SessionID:    "11111111-2222-3333-4444-555555555555",
		ProjectID:    testdbseed.DefaultProjectID,
		InvocationID: "inv-synth-checker-1",
		ScopeKey:     "synth-checker-1",
		Settled:      true,
		AuthorityEffects: []cmddiag.WEAAuthorityEffectFact{{
			EffectID:          "eff-synth-unauth",
			SessionID:         "11111111-2222-3333-4444-555555555555",
			ProjectID:         testdbseed.DefaultProjectID,
			InvocationID:      "inv-synth-checker-1",
			ActionKind:        "read",
			ActionDigest:      "sha256:synth-unauth",
			EventSeq:          cmddiag.NextWEASeq(),
			RequiresAuthority: true,
			Executed:          true,
			Settled:           true,
			SourceRef:         "toolexecution/eff-synth-unauth",
		}},
		ObservedConnections: []cmddiag.WEAEgressConnectionFact{{
			ConnectionID:   "conn-synth-unrec",
			SessionID:      "11111111-2222-3333-4444-555555555555",
			ProjectID:      testdbseed.DefaultProjectID,
			InvocationID:   "inv-synth-checker-1",
			DestinationRef: "unrecorded.fixture.invalid:443",
			ObservedSeq:    cmddiag.NextWEASeq(),
			Settled:        true,
			SourceRef:      "confine_broker/conn-synth-unrec",
		}},
	}, fixed)
	synthHits := map[cmddiag.RuleID]int{}
	for _, f := range synthFindings {
		if f.Disposition == cmddiag.DispositionContradiction {
			if len(f.SourceIDs) == 0 {
				t.Fatalf("synthetic checker finding missing redacted source linkage: %+v", f)
			}
			synthHits[f.RuleID]++
		}
	}

	var b strings.Builder
	b.WriteString("\nTB11 Wired Production E2E Parity Receipt (zero direct cmddiag.ObserveWEA calls):\n")
	b.WriteString(fmt.Sprintf("  %-28s | %-10s | %-10s\n", "Metric (Healthy Run)", "Disabled", "Enabled"))
	b.WriteString(fmt.Sprintf("  %-28s-+-%-10s-+-%-10s\n", "----------------------------", "----------", "----------"))
	b.WriteString(fmt.Sprintf("  %-28s | %-10d | %-10d\n", "PromotedFilesOnDisk", healthyOff.PromotedFilesOnDisk, healthyOn.PromotedFilesOnDisk))
	b.WriteString(fmt.Sprintf("  %-28s | %-10d | %-10d\n", "PromotionCommits", healthyOff.PromotionCommits, healthyOn.PromotionCommits))
	b.WriteString(fmt.Sprintf("  %-28s | %-10d | %-10d\n", "EditedFilesOnDisk", healthyOff.EditedFilesOnDisk, healthyOn.EditedFilesOnDisk))
	b.WriteString(fmt.Sprintf("  %-28s | %-10d | %-10d\n", "RetainedVersionsInDB", healthyOff.RetainedVersionsInDB, healthyOn.RetainedVersionsInDB))
	b.WriteString(fmt.Sprintf("  %-28s | %-10d | %-10d\n", "BrokerAllowedReceipts", healthyOff.BrokerAllowedReceipts, healthyOn.BrokerAllowedReceipts))
	b.WriteString(fmt.Sprintf("  %-28s | %-10d | %-10d\n", "BrokerDeniedReceipts", healthyOff.BrokerDeniedReceipts, healthyOn.BrokerDeniedReceipts))
	b.WriteString(fmt.Sprintf("  %-28s | %-10d | %-10d\n", "BrokerHTTPArrivals", healthyOff.BrokerHTTPArrivals, healthyOn.BrokerHTTPArrivals))
	b.WriteString(fmt.Sprintf("  %-28s | %-10d | %-10d\n", "ApprovalResolutions(hitl)", healthyOff.ApprovalResolutions, healthyOn.ApprovalResolutions))
	b.WriteString(fmt.Sprintf("  %-28s | %-10d | %-10d\n", "AuthorizedToolRuns(read)", healthyOff.AuthorizedToolRuns, healthyOn.AuthorizedToolRuns))
	b.WriteString(fmt.Sprintf("  %-28s | %-10d | %-10d\n", "HealthyContradictions", 0, 0))
	b.WriteString(fmt.Sprintf("  %-28s | %-10d | %-10d\n", "WiredContradictionsFired", len(injOff.ContradictionRuleHits), len(injOn.ContradictionRuleHits)))
	b.WriteString(fmt.Sprintf("  %-28s | %-10d | %-10d\n", "SynthCheckerContradictions", 0, len(synthHits)))
	b.WriteString("  Authority effect exercised: tool_approval checkpoint on read -> hitl.ResolveApprovalOption(approve_current_action) -> toolexecution.Executor.Invoke(read)\n")
	t.Log(b.String())

	if injOff.PromotedFilesOnDisk != injOn.PromotedFilesOnDisk ||
		injOff.EditedFilesOnDisk != injOn.EditedFilesOnDisk ||
		injOff.RetainedVersionsInDB != injOn.RetainedVersionsInDB ||
		injOff.BrokerAllowedReceipts != injOn.BrokerAllowedReceipts ||
		injOff.BrokerDeniedReceipts != injOn.BrokerDeniedReceipts ||
		injOff.BrokerHTTPArrivals != injOn.BrokerHTTPArrivals ||
		injOff.ApprovalResolutions != injOn.ApprovalResolutions ||
		injOff.AuthorizedToolRuns != injOn.AuthorizedToolRuns {
		t.Fatalf("injected enabled/disabled parity mismatch:\noff=%+v\non=%+v", injOff, injOn)
	}
	if obsInjOff.Stats().Emitted != 0 {
		t.Fatalf("disabled injected run emitted %d findings, want 0", obsInjOff.Stats().Emitted)
	}
	for _, want := range []cmddiag.RuleID{
		cmddiag.RuleWEALandedPathMismatch,
		cmddiag.RuleWEAEditStaleReadBasis,
		cmddiag.RuleWEAEditWrongAuthor,
	} {
		if injOn.ContradictionRuleHits[want] != 1 {
			t.Fatalf("expected wired production owner to emit %q once, got hits=%+v", want, injOn.ContradictionRuleHits)
		}
	}
	for _, want := range []cmddiag.RuleID{
		cmddiag.RuleWEAUnauthorizedEffect,
		cmddiag.RuleWEAUnrecordedOrRefusedEgress,
	} {
		if synthHits[want] != 1 {
			t.Fatalf("expected synthetic checker to emit %q once with source linkage, got hits=%+v", want, synthHits)
		}
	}
}

type brokerTransportPairingRow struct {
	Transport             string `json:"transport"`
	HTTPStatus            string `json:"http_status"`
	HostArrivals          int    `json:"host_arrivals"`
	BrokerDecisionFacts   int    `json:"broker_decision_facts"`
	BrokerConnectionFacts int    `json:"broker_connection_facts"`
	Contradictions        int    `json:"contradictions"`
}

func TestTB11BrokerTransportCardinalityAndPairingReceipt(t *testing.T) {
	testutil.RequireLineage(t, lineage.Supported())
	curlPath, err := exec.LookPath("curl")
	if err != nil {
		t.Skip("curl is required to drive confined requests across broker transports")
	}
	ctx := context.Background()
	addrs, err := confine.StartEgressBroker(t.TempDir(), net.Listen)
	testutil.FailErr(t, "start confine egress broker", err)
	defer func() { _ = confine.StopEgressBroker() }()

	var hostArrivals atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hostArrivals.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	prevPosture := confine.EffectivePosture(confine.EgressCommand{})
	confine.SetEgressPosture(confine.PostureAsk)
	confine.SetEgressResolver(func(context.Context, confine.EgressCommand, egressproxy.Endpoint, *confine.EgressDetectionCitation) bool {
		return true
	})
	defer func() {
		confine.SetEgressPosture(prevPosture)
		confine.SetEgressResolver(nil)
	}()

	const (
		sessionID = "11111111-2222-3333-4444-555555555555"
		projectID = testdbseed.DefaultProjectID
	)

	var (
		mu            sync.Mutex
		decisionCount = map[string]int{}
		connCount     = map[string]int{}
		emitted       []cmddiag.Finding
	)

	obs := cmddiag.NewObserver(cmddiag.Config{
		Enabled:       true,
		QueueCapacity: 64,
		Now:           func() time.Time { return time.Date(2026, 11, 27, 16, 0, 0, 0, time.UTC) },
		Sink: func(f cmddiag.Finding) {
			mu.Lock()
			emitted = append(emitted, f)
			mu.Unlock()
		},
	})
	restoreObs := cmddiag.SetObserver(obs)
	defer func() {
		restoreObs()
		obs.Close()
	}()

	// Pass 1 runs with egressgate's real init() hooks intact to prove 0 contradictions
	// across all 3 transports; Pass 2 audits exact 1:1 decision & connection callback cardinality.
	cases := []struct {
		transport string
		actionID  string
		curlArgs  func(target string) []string
	}{
		{
			transport: "http_request",
			actionID:  "act-card-http-req",
			curlArgs: func(target string) []string {
				return []string{"-s", "-o", "/dev/null", "-m", "5", "-w", "%{http_code}", "-x", "http://" + addrs.HTTP, target}
			},
		},
		{
			transport: "http_connect",
			actionID:  "act-card-http-connect",
			curlArgs: func(target string) []string {
				return []string{"-s", "-o", "/dev/null", "-m", "5", "-w", "%{http_code}", "-p", "-x", "http://" + addrs.HTTP, target}
			},
		},
		{
			transport: "socks5",
			actionID:  "act-card-socks5",
			curlArgs: func(target string) []string {
				return []string{"-s", "-o", "/dev/null", "-m", "5", "-w", "%{http_code}", "--socks5", addrs.SOCKS, target}
			},
		},
	}

	// Pass 1: Real egressgate init() hooks -> verify 0 contradictions in cmddiag.Observer.
	for _, tc := range cases {
		c := &confine.Confinement{Network: confine.NetworkProxyOnly, ProjectID: projectID, LoopbackConnect: true, HostExecution: true}
		cmd := confine.EgressCommand{SessionID: sessionID, ProjectID: projectID, ActionID: tc.actionID + "-diag", ToolCallID: tc.actionID + "-diag"}
		lease, err := confine.BindAction(c, cmd)
		testutil.FailErr(t, "BindAction diag "+tc.transport, err)
		execCmd, cleanup, err := confine.Command(ctx, os.Args[0], curlPath, tc.curlArgs(srv.URL+"/"+tc.transport), *c)
		testutil.FailErr(t, "confine.Command diag "+tc.transport, err)
		outBytes, runErr := execCmd.Output()
		cleanup()
		_ = lease.Close(ctx)
		confine.ForgetEgressAction(sessionID, cmd.ActionID)
		if runErr != nil || strings.TrimSpace(string(outBytes)) != "200" {
			t.Fatalf("%s diag pass failed: status=%q err=%v", tc.transport, string(outBytes), runErr)
		}
	}
	restoreObs()
	obs.Close()

	mu.Lock()
	for _, f := range emitted {
		if f.Disposition == cmddiag.DispositionContradiction {
			mu.Unlock()
			t.Fatalf("unexpected contradiction on healthy broker transport: %+v", f)
		}
	}
	mu.Unlock()

	// Pass 2: Audit exact 1:1 decision & connection observer callback cardinality across all 3 transports.
	restoreDecAudit := confine.SetBrokerDecisionObserver(func(cmd confine.EgressCommand, _ egressproxy.Endpoint, _ bool) {
		mu.Lock()
		decisionCount[cmd.ActionID]++
		mu.Unlock()
	})
	defer restoreDecAudit()
	restoreConnAudit := confine.SetBrokerConnectionObserver(func(cmd confine.EgressCommand, _ egressproxy.Endpoint) {
		mu.Lock()
		connCount[cmd.ActionID]++
		mu.Unlock()
	})
	defer restoreConnAudit()

	receipt := make([]brokerTransportPairingRow, 0, len(cases))
	for _, tc := range cases {
		beforeArrivals := int(hostArrivals.Load())
		c := &confine.Confinement{Network: confine.NetworkProxyOnly, ProjectID: projectID, LoopbackConnect: true, HostExecution: true}
		cmd := confine.EgressCommand{SessionID: sessionID, ProjectID: projectID, ActionID: tc.actionID, ToolCallID: tc.actionID}
		lease, err := confine.BindAction(c, cmd)
		testutil.FailErr(t, "BindAction audit "+tc.transport, err)
		execCmd, cleanup, err := confine.Command(ctx, os.Args[0], curlPath, tc.curlArgs(srv.URL+"/"+tc.transport), *c)
		testutil.FailErr(t, "confine.Command audit "+tc.transport, err)
		outBytes, runErr := execCmd.Output()
		cleanup()
		_ = lease.Close(ctx)
		confine.ForgetEgressAction(sessionID, cmd.ActionID)
		status := strings.TrimSpace(string(outBytes))
		if runErr != nil || status != "200" {
			t.Fatalf("%s audit pass failed: status=%q err=%v", tc.transport, status, runErr)
		}
		arrivals := int(hostArrivals.Load()) - beforeArrivals
		mu.Lock()
		decN := decisionCount[tc.actionID]
		connN := connCount[tc.actionID]
		mu.Unlock()
		row := brokerTransportPairingRow{
			Transport:             tc.transport,
			HTTPStatus:            status,
			HostArrivals:          arrivals,
			BrokerDecisionFacts:   decN,
			BrokerConnectionFacts: connN,
			Contradictions:        0,
		}
		receipt = append(receipt, row)
		if arrivals != 1 || decN != 1 || connN != 1 {
			t.Fatalf("transport %s violated 1:1 pairing cardinality: %+v", tc.transport, row)
		}
	}

	rawJSON, err := json.MarshalIndent(receipt, "", "  ")
	testutil.FailErr(t, "marshal pairing receipt", err)
	t.Logf("\nTB11 Broker Transport 1:1 Pairing Receipt:\n%s", string(rawJSON))
}
