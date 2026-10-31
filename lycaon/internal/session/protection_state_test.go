package session_test

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/approvalstate"
	"github.com/lycaon/lycaon/internal/session/protection"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/workeradmission"
	"github.com/lycaon/lycaon/internal/session/workercloseout"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/testbaseline"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestBuildProtectionStateUnknownVsActive(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep-based fixture is unix-oriented")
	}
	reg := bgprocess.NewRegistry(bgprocess.Config{MaxBackground: 4}, bgprocess.Hooks{})
	runner := hostcmd.NewRunner()
	dir := t.TempDir()
	handle, err := reg.StartPipeline(context.Background(), bgprocess.PipelineSpec{
		SessionID: "sess-prot", ProjectID: "proj",
		Request: hostcmd.Request{
			ProjectDir: dir,
			Stages:     []exec.Stage{{Name: "sleep", Args: []string{"30"}}},
			Launch: exec.AgentLaunch(exec.LaunchAgentCommand, "protection test", &confine.Confinement{
				Roots:   []string{dir},
				Network: confine.NetworkDirectIP,
			}),
		},
		Runner:     runner,
		Mode:       bgprocess.JobModeBackground,
		ToolCallID: "call-direct-1",
	})
	testutil.FailErr(t, "start", err)
	t.Cleanup(func() { _, _ = reg.Lifecycle.Stop("sess-prot", handle) })

	prot := protection.BuildProtectionState("sess-prot", reg)
	if prot == nil || len(prot.BackgroundDirect) != 1 {
		t.Fatalf("prot = %+v", prot)
	}
	if prot.BackgroundDirect[0].Status != api.SessionBackgroundDirectStatusActive {
		t.Fatalf("status = %s", prot.BackgroundDirect[0].Status)
	}
	if prot.BackgroundDirect[0].ToolCallID != "call-direct-1" {
		t.Fatalf("tool_call_id = %q", prot.BackgroundDirect[0].ToolCallID)
	}

	testutil.FailErr(t, "mark unknown", reg.MarkDirectIPLivenessUnknown("sess-prot", handle))
	prot = protection.BuildProtectionState("sess-prot", reg)
	if prot.BackgroundDirect[0].Status != api.SessionBackgroundDirectStatusUnknown {
		t.Fatalf("want unknown, got %s", prot.BackgroundDirect[0].Status)
	}
}

func TestProtectionStateReconstructEmitsOnceWithoutDestinations(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep-based fixture is unix-oriented")
	}
	reg := bgprocess.NewRegistry(bgprocess.Config{MaxBackground: 4}, bgprocess.Hooks{})
	runner := hostcmd.NewRunner()
	dir := t.TempDir()
	handle, err := reg.StartPipeline(context.Background(), bgprocess.PipelineSpec{
		SessionID: "sess-rec", ProjectID: "proj",
		Request: hostcmd.Request{
			ProjectDir: dir,
			Stages:     []exec.Stage{{Name: "sleep", Args: []string{"30"}}},
			Launch: exec.AgentLaunch(exec.LaunchAgentCommand, "reconstruct test", &confine.Confinement{
				Roots:   []string{dir},
				Network: confine.NetworkDirectIP,
			}),
		},
		Runner: runner,
		Mode:   bgprocess.JobModeBackground, ToolCallID: "call-9",
	})
	testutil.FailErr(t, "start", err)
	t.Cleanup(func() { _, _ = reg.Lifecycle.Stop("sess-rec", handle) })

	mgr := session.NewHost(sessionstore.NewMemory(), session.Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	mgr.SetBackgroundRegistry(reg)
	var emits atomic.Int32
	mgr.Chats.Protection.SetDirectIPReconstructHook(func(sessionID, gotHandle, toolCallID string) {
		emits.Add(1)
		if sessionID != "sess-rec" || gotHandle != handle || toolCallID != "call-9" {
			t.Errorf("unexpected reconstruct args: %s %s %s", sessionID, gotHandle, toolCallID)
		}
	})

	_ = mgr.Chats.Protection.ProtectionStateForSession("sess-rec")
	_ = mgr.Chats.Protection.ProtectionStateForSession("sess-rec")
	if got := emits.Load(); got != 1 {
		t.Fatalf("reconstruct emits = %d want 1", got)
	}

	var replacementEmits atomic.Int32
	mgr.Chats.Protection.SetDirectIPReconstructHook(func(_, _, _ string) {
		replacementEmits.Add(1)
	})
	_ = mgr.Chats.Protection.ProtectionStateForSession("sess-rec")
	if got := replacementEmits.Load(); got != 1 {
		t.Fatalf("replacement hook emits = %d want 1", got)
	}
}

func TestRevokeChatGrantAfterRecoveryKeepsBackgroundWarning(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep-based fixture is unix-oriented")
	}
	reg := bgprocess.NewRegistry(bgprocess.Config{MaxBackground: 4}, bgprocess.Hooks{})
	runner := hostcmd.NewRunner()
	dir := t.TempDir()
	handle, err := reg.StartPipeline(context.Background(), bgprocess.PipelineSpec{
		SessionID: "sess-rev", ProjectID: "proj",
		Request: hostcmd.Request{
			ProjectDir: dir,
			Stages:     []exec.Stage{{Name: "sleep", Args: []string{"30"}}},
			Launch: exec.AgentLaunch(exec.LaunchAgentCommand, "revocation test", &confine.Confinement{
				Roots:   []string{dir},
				Network: confine.NetworkDirectIP,
			}),
		},
		Runner: runner,
		Mode:   bgprocess.JobModeBackground,
	})
	testutil.FailErr(t, "start", err)
	t.Cleanup(func() { _, _ = reg.Lifecycle.Stop("sess-rev", handle) })

	rt := approvalstate.NewSocketCapabilityRuntime()
	approved, resolved := tempSocket(t)
	rt.GrantChat("sess-rev", confine.SocketGrant{ApprovedPath: approved, ResolvedPath: resolved}, "grant_t", "cp", "d", nil)
	if len(rt.AppliedGrants("sess-rev")) != 1 {
		t.Fatal("expected live chat grant")
	}
	if _, ok := rt.RevokeByID("grant_t"); !ok {
		t.Fatal("revoke found no grant")
	}
	if len(rt.AppliedGrants("sess-rev")) != 0 {
		t.Fatal("revoke must affect next boundary")
	}
	prot := protection.BuildProtectionState("sess-rev", reg)
	if prot == nil || len(prot.BackgroundDirect) != 1 {
		t.Fatalf("still-running direct process must keep warning, prot=%+v", prot)
	}
}

func TestProtectionStateReportsStartupOverrides(t *testing.T) {
	t.Setenv("LYCAON_SANDBOX", "")
	t.Setenv("LYCAON_BYPASS_APPROVALS", "")
	t.Setenv("LYCAON_SANDBOX_DENY_READ", "off")
	root := t.TempDir()
	t.Setenv("LYCAON_SANDBOX_WRITE_ROOTS", root)
	prot := protection.BuildProtectionState("session", nil)
	if prot == nil || !prot.ControlPlaneReadsAllowed || len(prot.AdditionalWriteRoots) != 1 {
		t.Fatalf("startup overrides = %+v", prot)
	}
	if prot.SandboxBypass {
		t.Fatal("partial overrides reported as full bypass")
	}
	if len(confine.SecretReadDenyRoots()) != 0 {
		t.Fatal("read override disagrees with enforced read roots")
	}
}

// tempSocket listens on a short unix socket path and returns its approved and resolved paths.
func tempSocket(t *testing.T) (approved, resolved string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "sk") //nolint:usetesting // Short socket path.
	testutil.FailErr(t, "MkdirTemp", err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", path)
	testutil.FailErr(t, "listen unix", err)
	t.Cleanup(func() { _ = ln.Close() })
	resolved, err = filepath.EvalSymlinks(path)
	testutil.FailErr(t, "eval symlinks", err)
	return path, resolved
}

func TestTaskScopeCapExceeded(t *testing.T) {
	caps := workeradmission.ParallelTaskCaps{MaxRead: spawn.DefaultMaxReadTaskWorkers, MaxWrite: spawn.DefaultMaxWriteTaskWorkers}
	active := make([]api.WorkerTask, caps.MaxWrite)
	for i := range active {
		active[i] = api.WorkerTask{Scope: &api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"pkg/a.go"}}}
	}
	if !workeradmission.TaskScopeCapExceeded(active, api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"pkg/b.go"}}, caps) {
		t.Fatal("expected write cap exceeded")
	}
}

// Overlay diffs retain their source generation in completion proof.
func TestOverlayChangedPathsFeedWorkerProof(t *testing.T) {
	dir := t.TempDir()
	scope := api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"space_pacifism/main.py"}}
	baseline := testbaseline.Capture(t, dir)
	if err := os.MkdirAll(filepath.Join(dir, "space_pacifism"), 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "space_pacifism/main.py"), []byte("new\n"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	task := &api.WorkerTask{
		AgentType: "implementer",
		ProjectID: testdbseed.DefaultProjectID, WorkspacePath: dir,
		WorkspaceRoot:         dir, // claimed branch (test uses same tree)
		Scope:                 &scope,
		WorkspaceBaselinePath: baseline,
	}
	changed := session.OverlayWorkspaceChangedPaths(t.Context(), task, nil)
	proof := workercompletion.BuildWorkerCompletionProof(nil, changed, workercompletion.SourceRevision{
		Revision: "revision-1", RootDigest: "root-1",
	})
	if len(proof.ChangedPaths) != 1 || proof.ChangedPaths[0] != "space_pacifism/main.py" {
		t.Fatalf("ChangedPaths = %v, want [space_pacifism/main.py]", proof.ChangedPaths)
	}
	if proof.SourceRevision != "revision-1" || proof.SourceRootDigest != "root-1" {
		t.Fatalf("source generation = %q/%q", proof.SourceRevision, proof.SourceRootDigest)
	}
}

func TestFormatWorkerCompletionEnvelopeIncludesProofAndReportJSON(t *testing.T) {
	out := workercompletion.FormatWorkerCompletionEnvelope(workercompletion.WorkerCompletionEnvelope{
		JobID:   "job-1",
		State:   "complete",
		Summary: "done",
		Body:    "done",
		Digest:  "Host worker digest",
		Proof: workercompletion.WorkerCompletionProof{
			ChangedPaths: []string{"src/a.go"},
			ReceiptCount: 2,
		},
		Report: workercompletion.WorkerCompletionReport{
			LegStatus:     "complete",
			FilesModified: []string{"src/a.go"},
			ObjectivesMet: []string{"added handler"},
			Brief:         "done",
		},
	})
	if strings.Contains(out, "<digest>") {
		t.Fatalf("unexpected <digest>: %q", out)
	}
	if strings.Contains(out, "<task_result>") {
		t.Fatalf("task_result must be omitted when it equals summary: %q", out)
	}
	if !strings.Contains(out, "<proof_json>") || !strings.Contains(out, "src/a.go") {
		t.Fatalf("missing proof_json: %q", out)
	}
	if !strings.Contains(out, "<report_json>") || !strings.Contains(out, "objectives_met") {
		t.Fatalf("missing report_json: %q", out)
	}
}

func TestWorkerFinalizationSuspendsDespiteBlockedCompletion(t *testing.T) {
	resolver := &stubSummaryResolver{msgs: append(scoutSurveyFixtureMessages(), completeLegToolRow(map[string]any{
		"leg_status": "blocked", "brief": "Parser blocked writes; decision requested.",
	}))}
	decisionChecks := 0
	opts := finalizeOpts(resolver, "child", workercloseout.WorkerSummaryFinalizeOpts{
		DecisionPending: func(context.Context, string) bool { decisionChecks++; return true },
	})
	out, workerEvalErr := workercloseout.FinalizeWorkerSummaryForChild(t.Context(), resolver, resolver, "child", "implementer", opts)
	testutil.FailErr(t, "evaluate worker completion", workerEvalErr)
	t.Logf("status=%s provenance=%s pending-decision checks=%d", out.Status, out.Provenance, decisionChecks)
	if out.Status != "needs_decision" || decisionChecks == 0 {
		t.Fatalf("unexpected result: %+v checks=%d", out, decisionChecks)
	}
}

func TestDecisionRequestedDuringCloseoutStopsFinalizationPrompts(t *testing.T) {
	resolver := &stubSummaryResolver{msgs: scoutSurveyFixtureMessages()}
	opts := finalizeOpts(resolver, "child", workercloseout.WorkerSummaryFinalizeOpts{
		DecisionPending: func(context.Context, string) bool { return resolver.prompts > 0 },
	})
	out, workerEvalErr := workercloseout.FinalizeWorkerSummaryForChild(t.Context(), resolver, resolver, "child", "implementer", opts)
	testutil.FailErr(t, "evaluate worker completion", workerEvalErr)
	if out.Status != "needs_decision" || resolver.prompts != 1 {
		t.Fatalf("status=%s prompts=%d", out.Status, resolver.prompts)
	}
}

type failedFinalizationLedger struct{ err error }

func (r failedFinalizationLedger) LoadLedger(context.Context, string) (evidence.Ledger, error) {
	return evidence.Ledger{}, r.err
}

type failedFinalizationTranscript struct {
	*stubSummaryResolver
	err error
}

func (r failedFinalizationTranscript) GetWorkerJobMessages(context.Context, string, string) ([]api.Message, error) {
	return nil, r.err
}

func TestWorkerFinalizationStorageFailureDoesNotBecomeCompletion(t *testing.T) {
	for _, failureAt := range []string{"ledger", "transcript"} {
		for _, canceled := range []bool{false, true} {
			t.Run(failureAt+map[bool]string{false: "/normal", true: "/canceled"}[canceled], func(t *testing.T) {
				failure := errors.New("worker storage read failed")
				base := &stubSummaryResolver{msgs: []api.Message{completeLegToolRow(map[string]any{
					"leg_status": "complete", "brief": "The assignment is complete",
				})}}
				opts := finalizeOpts(base, "child", workercloseout.WorkerSummaryFinalizeOpts{})
				var resolver workercloseout.WorkerSummaryResolver = base
				if failureAt == "ledger" {
					opts.Ledger = failedFinalizationLedger{err: failure}
				} else {
					resolver = failedFinalizationTranscript{stubSummaryResolver: base, err: failure}
				}
				var result workercloseout.WorkerSummaryOutcome
				var err error
				if canceled {
					result, err = workercloseout.FinalizeWorkerSummaryForCanceled(t.Context(), resolver, base, "child", "path-explorer", "canceled", opts)
				} else {
					result, err = workercloseout.FinalizeWorkerSummaryForChild(t.Context(), resolver, base, "child", "path-explorer", opts)
				}
				if !errors.Is(err, failure) {
					t.Fatalf("finalization failure = %v, want original cause", err)
				}
				if result.Summary != "" || result.HintCode != "" || result.HostAssembled {
					t.Fatalf("storage failure produced deliverable completion: %+v", result)
				}
			})
		}
	}
}
