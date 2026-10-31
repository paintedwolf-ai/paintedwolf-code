package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/cost"
	"github.com/lycaon/lycaon/internal/cost/costtest"
	"github.com/lycaon/lycaon/internal/inspector"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/llm"
	sessiondecisions "github.com/lycaon/lycaon/internal/session/decisions"
	"github.com/lycaon/lycaon/internal/session/spendguard"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/verification"
	"github.com/lycaon/lycaon/internal/session/workercontext"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testbaseline"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func sourceEvidenceCloseoutHarness(t *testing.T) (*Host, *api.Session, []api.Message) {
	t.Helper()
	mgr, sess := newSynthesisDelayManager(t)
	mgr.SetDataDir(t.TempDir())
	mgr.Verification.SetEvidenceStore(inspector.NewJSONLStore(inspector.DefaultEvidenceDir))
	mgr.Verification.SetRevisionSource(func(_ context.Context, root string) (string, string) {
		return invocation.SourceRevisionForRoot(root)
	})
	sess.ProjectID = "source-evidence-project"
	sess.WorkspacePath = t.TempDir()
	history := workSince([]api.Message{{Role: api.MessageRoleUser, Content: "change it"}})
	return mgr, sess, history
}

func TestSourceEvidenceCloseoutAllowsRoutineWorkWithoutAssessment(t *testing.T) {
	mgr, sess, history := sourceEvidenceCloseoutHarness(t)
	reject, blocked := mgr.Coordinator.Guards.SourceEvidence(
		context.Background(), sess, history, "implement_investigate", true,
	)
	if blocked || reject != nil {
		t.Fatalf("reject=%v blocked=%v", reject, blocked)
	}
}

func TestSourceEvidenceCloseoutAcceptsCurrentPass(t *testing.T) {
	mgr, sess, history := sourceEvidenceCloseoutHarness(t)
	mgr.SetWorkflowDomains(workflowDomainFixture(verifyWorkflowStub{required: true}))
	recordVerify(t, mgr, sess, "go test ./...", 0)
	if _, blocked := mgr.Coordinator.Guards.SourceEvidence(
		context.Background(), sess, history, "implement_investigate", true,
	); blocked {
		t.Fatal("current passing verify should release closeout")
	}
}

func TestSourceEvidenceCloseoutAcceptsCurrentCommand(t *testing.T) {
	mgr, sess, history := sourceEvidenceCloseoutHarness(t)
	mgr.SetWorkflowDomains(workflowDomainFixture(verifyWorkflowStub{required: true}))
	recordCommand(t, mgr, sess, "./ntp_check.py --json", 0)
	if _, blocked := mgr.Coordinator.Guards.SourceEvidence(
		context.Background(), sess, history, "implement_investigate", true,
	); blocked {
		t.Fatal("current passing command should release undeclared closeout")
	}
}

func TestSourceEvidenceCloseoutAllowsExplicitUnverifiedAfterBoundedAttempts(t *testing.T) {
	mgr, sess, history := sourceEvidenceCloseoutHarness(t)
	mgr.SetWorkflowDomains(workflowDomainFixture(verifyWorkflowStub{required: true}))
	for range verification.MaxAttemptsPerRun {
		recordVerify(t, mgr, sess, "go test ./...", 1)
	}
	if _, blocked := mgr.Coordinator.Guards.SourceEvidence(
		context.Background(), sess, history, "implement_investigate", true,
	); blocked {
		t.Fatal("bounded failed attempts should permit a partial, unverified closeout")
	}
}

type sourceEvidenceWorkerQueue struct {
	noopWorkerBranchClaim
	task *api.WorkerTask
}

func (q sourceEvidenceWorkerQueue) Get(jobID string) (*api.WorkerTask, bool) {
	if q.task == nil || q.task.ID != jobID {
		return nil, false
	}
	cp := *q.task
	return &cp, true
}

func (q sourceEvidenceWorkerQueue) ListBySession(context.Context, string, string, ...api.WorkerStatus) ([]api.WorkerTask, error) {
	return nil, nil
}

func workerSourceEvidenceCloseoutHarness(t *testing.T) (*Host, *api.Session, *api.WorkerTask, []api.Message) {
	t.Helper()
	mgr, parent := newSynthesisDelayManager(t)
	mgr.Verification.SetRevisionSource(func(_ context.Context, root string) (string, string) {
		return invocation.SourceRevisionForRoot(root)
	})
	primary := t.TempDir()
	overlay := filepath.Join(primary, settingsoverlay.DirName(), "overlays", "job-worker")
	testutil.FailErr(t, "MkdirAll", os.MkdirAll(overlay, 0o755))
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(overlay, "a.go"), []byte("package a\n"), 0o644))
	scope := api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"a.go"}}
	snap := testbaseline.Capture(t, primary)
	raw := snap
	child, err := mgr.Coordinator.Context.Sessions.(Store).CreateChild(context.Background(), parent, api.SpawnChildRequest{AgentType: "implementer"})
	testutil.FailErr(t, "CreateChild", err)
	task := &api.WorkerTask{
		ID: "job-worker", ParentSessionID: parent.ID, ChildSessionID: child.ID,
		WorkspacePath: primary, WorkspaceRoot: overlay, AgentType: "implementer",
		Status: api.WorkerStatusRunning, MergeStatus: api.WorkerMergeStatusPending,
		Scope: &scope, WorkspaceBaselinePath: raw,
	}
	mgr.SetWorkerQueue(sourceEvidenceWorkerQueue{task: task})
	return mgr, child, task, nil
}

func TestWorkerSourceEvidenceCloseoutDoesNotGateOnValidation(t *testing.T) {
	for _, selected := range []string{"", "project-check"} {
		for _, verdict := range []string{"", api.SourceVerdictPassed, api.SourceVerdictFailed} {
			t.Run("selected="+selected+"/verdict="+verdict, func(t *testing.T) {
				mgr, child, task, history := workerSourceEvidenceCloseoutHarness(t)
				mgr.Verification.SetVerifyConfig(stubVerifyConfig{cmd: selected})
				mgr.SetWorkflowDomains(workflowDomainFixture(verifyWorkflowStub{required: true}))
				if verdict != "" {
					history = append(history, api.Message{
						Role: api.MessageRoleTool,
						ToolResult: &api.ToolResult{
							Invocation: &api.InvocationReceipt{
								ID: "receipt-check", Tool: "verify",
								Status: api.InvocationStatusCompleted, SourceVerdict: verdict,
							},
						},
					})
				}
				reject, blocked := mgr.Coordinator.Guards.SourceEvidence(
					workercontext.WithJob(t.Context(), task.ID), child, history, "implement_investigate", true,
				)
				if blocked || reject != nil {
					t.Fatalf("worker validation blocked closeout: reject=%v blocked=%v", reject, blocked)
				}
			})
		}
	}
}

func TestWorkerDecisionPauseAllowsHandoff(t *testing.T) {
	mgr, child, task, history := workerSourceEvidenceCloseoutHarness(t)
	ctx := workercontext.WithJob(t.Context(), task.ID)
	if _, blocked := mgr.Coordinator.Guards.SourceEvidence(ctx, child, history, "", true); blocked {
		t.Fatal("routine worker closeout must not require validation")
	}
	decisions := sessiondecisions.NewMemory()
	mgr.SetDecisionStore(decisions)
	testutil.FailErr(t, "record worker decision", decisions.Put(ctx, api.WorkerDecisionRequest{ChildSessionID: child.ID, WorkerID: task.ID, Question: "Choose the contract", Options: []string{"A", "B"}}))
	reject, blocked := mgr.Coordinator.Guards.BeforeFinish(ctx, child, history, "", "Waiting for a decision", "", true, []string{"request_decision"}, true)
	if blocked || reject != nil {
		t.Fatalf("accepted decision forced worker closeout: blocked=%v reject=%v", blocked, reject)
	}
}

func (sourceEvidenceWorkerQueue) ListPendingOutcomes(context.Context) ([]api.WorkerTask, error) {
	return nil, nil
}

type stubSpendPricer struct {
	usd      float64
	unpriced bool
	noCharge bool
}

func (s stubSpendPricer) NoCharge(_, _ string) bool { return s.noCharge }

func (s stubSpendPricer) EstimateCost(providerID, model string, usage cost.TokenUsage) (cost.CostEstimate, error) {
	if s.unpriced {
		return cost.CostEstimate{Currency: "USD", Unpriced: true}, nil
	}
	return cost.CostEstimate{EstimatedUSD: s.usd, Currency: "USD"}, nil
}

func TestCheckSpendCeiling(t *testing.T) {
	ctx := context.Background()

	t.Run("disabled", func(t *testing.T) {
		tracker := costtest.NewTracker(t, stubSpendPricer{usd: 0.01})
		usd := 9.0
		testutil.FailErr(t, "RecordUsage", tracker.RecordUsage(ctx, cost.UsageEvent{
			SessionID: "s1", Caller: cost.CallerCoordinator, PromptTokens: 100, EstimatedNanoUSD: costtest.NanoUSD(t, usd),
		}))
		lim := settings.DefaultSessionLimits()
		lim.SpendCeilingEnabled = false
		lim.SessionSpendCeilingUSD = 1
		mgr := NewHost(store.NewMemory(), Models{Client: llm.NewMockProvider(&llm.MockConfig{}), Provider: nil, Limits: lim, Cost: tracker}, tools.NewStubRegistry())
		sess := &api.Session{ID: "s1", ProjectID: "p1"}
		if err := mgr.Runner.Spend.Check(ctx, "s1", sess); err != nil {
			t.Fatalf("disabled ceiling must not enforce: %v", err)
		}
	})

	t.Run("enabled_unpriced", func(t *testing.T) {
		tracker := costtest.NewTracker(t, stubSpendPricer{unpriced: true})
		testutil.FailErr(t, "RecordUsage", tracker.RecordUsage(ctx, cost.UsageEvent{
			SessionID: "s2", Caller: cost.CallerCoordinator, PromptTokens: 100, Unpriced: true,
		}))
		lim := settings.DefaultSessionLimits()
		lim.SpendCeilingEnabled = true
		lim.SessionSpendCeilingUSD = 1
		mgr := NewHost(store.NewMemory(), Models{Client: llm.NewMockProvider(&llm.MockConfig{}), Provider: nil, Limits: lim, Cost: tracker}, tools.NewStubRegistry())
		sess := &api.Session{ID: "s2", ProjectID: "p1"}
		if err := mgr.Runner.Spend.Check(ctx, "s2", sess); err != nil {
			t.Fatalf("unpriced must not enforce: %v", err)
		}
	})

	t.Run("enabled_priced_under", func(t *testing.T) {
		tracker := costtest.NewTracker(t, stubSpendPricer{usd: 0.01})
		usd := 0.5
		testutil.FailErr(t, "RecordUsage", tracker.RecordUsage(ctx, cost.UsageEvent{
			SessionID: "s3", Caller: cost.CallerCoordinator, PromptTokens: 10, EstimatedNanoUSD: costtest.NanoUSD(t, usd),
		}))
		lim := settings.DefaultSessionLimits()
		lim.SpendCeilingEnabled = true
		lim.SessionSpendCeilingUSD = 5
		mgr := NewHost(store.NewMemory(), Models{Client: llm.NewMockProvider(&llm.MockConfig{}), Provider: nil, Limits: lim, Cost: tracker}, tools.NewStubRegistry())
		sess := &api.Session{ID: "s3", ProjectID: "p1"}
		if err := mgr.Runner.Spend.Check(ctx, "s3", sess); err != nil {
			t.Fatalf("under ceiling must pass: %v", err)
		}
	})

	t.Run("enabled_priced_reached", func(t *testing.T) {
		tracker := costtest.NewTracker(t, stubSpendPricer{usd: 0.01})
		usd := 5.5
		testutil.FailErr(t, "RecordUsage", tracker.RecordUsage(ctx, cost.UsageEvent{
			SessionID: "s4", Caller: cost.CallerCoordinator, PromptTokens: 10, EstimatedNanoUSD: costtest.NanoUSD(t, usd),
		}))
		testutil.FailErr(t, "RecordUsage unpriced", tracker.RecordUsage(ctx, cost.UsageEvent{
			SessionID: "worker-1", ParentSessionID: "s4",
			Caller: cost.CallerWorker, PromptTokens: 20, Unpriced: true,
		}))
		lim := settings.DefaultSessionLimits()
		lim.SpendCeilingEnabled = true
		lim.SessionSpendCeilingUSD = 5
		mgr := NewHost(store.NewMemory(), Models{Client: llm.NewMockProvider(&llm.MockConfig{}), Provider: nil, Limits: lim, Cost: tracker}, tools.NewStubRegistry())
		sess := &api.Session{ID: "s4", ProjectID: "p1"}
		err := mgr.Runner.Spend.Check(ctx, "s4", sess)
		if !errors.Is(err, spendguard.ErrCeiling) {
			t.Fatalf("err = %v want spendguard.ErrCeiling", err)
		}
		var reached *spendguard.CeilingReached
		if !errors.As(err, &reached) || reached.CeilingUSD != 5 || reached.SpentUSD != 5.5 || reached.UnpricedTokens != 20 || reached.Coverage != api.CostEstimateLowerBound {
			t.Fatalf("reached = %+v", reached)
		}
	})
}

func TestSpendCeilingRetainsUnknownChargedCalls(t *testing.T) {
	ctx := t.Context()
	tracker := costtest.NewTracker(t, stubSpendPricer{})
	usd := 5.5
	testutil.FailErr(t, "record priced call", tracker.RecordUsage(ctx, cost.UsageEvent{SessionID: "s", Caller: cost.CallerCoordinator, PromptTokens: 100, EstimatedNanoUSD: costtest.NanoUSD(t, usd)}))
	testutil.FailErr(t, "begin charged call", tracker.BeginCall(ctx, cost.UsageEvent{ID: "missing", SessionID: "s", ProviderID: "cloud", Caller: cost.CallerCoordinator}))
	testutil.FailErr(t, "mark missing usage", tracker.MarkCallUnknown(ctx, "missing"))
	lim := settings.DefaultSessionLimits()
	lim.SpendCeilingEnabled = true
	lim.SessionSpendCeilingUSD = 5
	mgr := NewHost(store.NewMemory(), Models{Client: llm.NewMockProvider(&llm.MockConfig{}), Provider: nil, Limits: lim, Cost: tracker}, tools.NewStubRegistry())
	err := mgr.Runner.Spend.Check(ctx, "s", &api.Session{ID: "s", ProjectID: "p"})
	var reached *spendguard.CeilingReached
	if !errors.As(err, &reached) || reached.UnknownChargedCalls != 1 || reached.UnpricedTokens != 0 || reached.Coverage != api.CostEstimateLowerBound {
		t.Fatalf("ceiling error=%+v", reached)
	}
}

func TestReviewedStopTransitionPreservesRuntimeOnRejection(t *testing.T) {
	for _, accepted := range []bool{false, true} {
		t.Run(map[bool]string{false: "rejected", true: "accepted"}[accepted], func(t *testing.T) {
			ctx := t.Context()
			st := store.NewMemory()
			mgr := NewHost(st, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
			sess, err := st.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
			testutil.FailErr(t, "create session", err)
			testutil.FailErr(t, "mark busy", st.SetSessionStatus(ctx, sess.ID, api.SessionStatusBusy))
			token, err := mgr.Chats.Gate.Capture(ctx, sess.ID)
			testutil.FailErr(t, "capture current turn", err)
			refusal := errors.New("stale workflow revision")
			err = mgr.Stops.WithSessionTreeStop(ctx, sess.ID, "reviewed exit", func(context.Context) error {
				if !accepted {
					return refusal
				}
				return nil
			})
			if accepted {
				testutil.FailErr(t, "stop accepted exit", err)
			} else if !errors.Is(err, refusal) {
				t.Fatalf("rejected exit error = %v", err)
			}
			current, err := st.Get(ctx, sess.ID)
			testutil.FailErr(t, "read session", err)
			want := api.SessionStatusBusy
			if accepted {
				want = api.SessionStatusIdle
			}
			if current.Status != want || mgr.Chats.Gate.MayDrain(token) == accepted {
				t.Fatalf("status=%s drain=%v accepted=%v", current.Status, mgr.Chats.Gate.MayDrain(token), accepted)
			}
			if mgr.Chats.Gate.InProgress(ctx, sess.ID) {
				t.Fatal("stop barrier retained after transition")
			}
			testutil.FailErr(t, "admit next turn", mgr.Chats.Gate.WithSessionTreeAdmission(ctx, sess.ID, func() error { return nil }))
		})
	}
}

func TestReviewedStopTransitionStillStopsOtherSessionWorkflows(t *testing.T) {
	ctx := t.Context()
	st := store.NewMemory()
	mgr := NewHost(st, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	workflows := &stubSessionWorkflowStop{}
	mgr.Stops.SetWorkflowStop(workflows)
	root, err := st.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create root", err)
	child, err := st.CreateChild(ctx, root, api.SpawnChildRequest{AgentType: "implementer"})
	testutil.FailErr(t, "create child", err)
	testutil.FailErr(t, "exit root workflow", mgr.Stops.WithSessionTreeStop(ctx, root.ID, "reviewed exit", func(context.Context) error { return nil }))
	if len(workflows.sessions) != 1 || workflows.sessions[0] != child.ID {
		t.Fatalf("workflow stops = %v, want child only after committed root transition", workflows.sessions)
	}
	for _, id := range []string{root.ID, child.ID} {
		current, err := st.Get(ctx, id)
		testutil.FailErr(t, "read stopped session", err)
		if current.Status != api.SessionStatusIdle {
			t.Fatalf("session %s status = %s", id, current.Status)
		}
	}
}
