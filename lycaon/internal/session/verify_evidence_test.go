package session

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/inspector"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/verification"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type stubVerifyConfig struct{ cmd string }

var exitZero = 0

func (s stubVerifyConfig) VerifyTestCommand(string) string { return s.cmd }

// verifyGateHarness creates a session with persistent evidence.
func verifyGateHarness(t *testing.T, declared string) (*Manager, *api.Session, []api.Message) {
	t.Helper()
	ctx := context.Background()
	mgr := NewManager(store.NewMemory(), nil, nil, settings.DefaultSessionLimits())
	mgr.SetDataDir(t.TempDir())
	mgr.Verification.SetEvidenceStore(inspector.NewJSONLStore(inspector.DefaultEvidenceDir))
	mgr.Verification.SetRevisionSource(func(_ context.Context, root string) (string, string) {
		return invocation.SourceRevisionForRoot(root)
	})
	if declared != "" {
		mgr.Verification.SetVerifyConfig(stubVerifyConfig{cmd: declared})
	}
	sess, err := mgr.store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, "")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	sess.ProjectID = "verify-proj"
	sess.WorkspacePath = t.TempDir()
	history := []api.Message{{Role: api.MessageRoleUser, Content: "do the work", CreatedAt: time.Now().Add(-time.Minute)}}
	return mgr, sess, history
}

// statedRun is the subsystem owner's invocation result.
func statedRun(command string, exitCode int) tools.SourceRunCapture {
	verdict := api.SourceVerdictPassed
	if exitCode != 0 {
		verdict = api.SourceVerdictFailed
	}
	return tools.SourceRunCapture{Command: command, ExitCode: exitCode, Verdict: verdict, IsCheck: true, Cwd: "."}
}

func recordVerify(t *testing.T, m *Manager, sess *api.Session, command string, exitCode int) {
	t.Helper()
	run := statedRun(command, exitCode)
	run.SourceRevision, run.SourceRootDigest = invocation.SourceRevisionForRoot(sess.WorkspacePath)
	m.Verification.RecordSourceRunEvidence(context.Background(), sess.ID, sess, verification.ProducerVerify, run)
}

func recordCommand(t *testing.T, m *Manager, sess *api.Session, command string, exitCode int) {
	t.Helper()
	run := statedRun(command, exitCode)
	run.SourceRevision, run.SourceRootDigest = invocation.SourceRevisionForRoot(sess.WorkspacePath)
	m.Verification.RecordSourceRunEvidence(context.Background(), sess.ID, sess, verification.ProducerCommand, run)
}

// workSince appends a completed write so the turn counts as implementation work.
func workSince(history []api.Message) []api.Message {
	return append(history,
		api.Message{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{ID: "c1", Name: "write", Args: map[string]any{"path": "a.go"}}}},
		api.Message{Role: api.MessageRoleTool, Content: "ok", ToolResult: &api.ToolResult{
			Outcome:  api.ToolResultOutcomeCompleted,
			FileEdit: &api.FileEditSnapshot{Path: "a.go"},
		}},
	)
}

type verifyWorkflowStub struct {
	stubWorkflowManifest
	required bool
}

func (s verifyWorkflowStub) ActivePhaseRequiresEvidence(context.Context, string, string) bool {
	return s.required
}

func TestWorkflowVerifyGateStateRequiresExplicitWorkflowEvidence(t *testing.T) {
	mgr, sess, history := verifyGateHarness(t, "")
	history = workSince(history)
	if required, passed, repair, unverified := mgr.Verification.WorkflowGateState(context.Background(), sess, history); required || passed || repair || unverified {
		t.Fatalf("changed source invented a workflow gate: (%v,%v,%v,%v)", required, passed, repair, unverified)
	}

	mgr.SetWorkflowDomains(workflowDomainFixture(verifyWorkflowStub{required: true}))
	if required, passed, repair, unverified := mgr.Verification.WorkflowGateState(context.Background(), sess, history); !required || passed || repair || unverified {
		t.Fatalf("required workflow gate = (%v,%v,%v,%v) want (true,false,false,false)", required, passed, repair, unverified)
	}
}

func TestVerifyGateState_undeclaredReadOnlyHasNothingToVerify(t *testing.T) {
	mgr, sess, history := verifyGateHarness(t, "")
	if p, r, u := mgr.Verification.GateState(context.Background(), sess, history); p || r || u {
		t.Fatalf("no command, no work = (pass %v, repair %v, unverified %v) want all false", p, r, u)
	}
	// An undeclared project can still provide explicit proof through verify.
	recordVerify(t, mgr, sess, "go test ./...", 0)
	if p, _, _ := mgr.Verification.GateState(context.Background(), sess, history); !p {
		t.Fatal("undeclared: explicit passing verify should cover the current revision")
	}
}

func TestVerifyGateState_undeclaredWithWorkIsUnverified(t *testing.T) {
	mgr, sess, history := verifyGateHarness(t, "")
	history = workSince(history)
	if p, r, u := mgr.Verification.GateState(context.Background(), sess, history); p || r || u {
		t.Fatalf("work, no attempt = (pass %v, repair %v, unverified %v) want all false", p, r, u)
	}
	recordVerify(t, mgr, sess, "go test ./...", 0)
	if p, _, u := mgr.Verification.GateState(context.Background(), sess, history); !p || u {
		t.Fatalf("undeclared explicit pass = (pass %v, unverified %v) want (true,false)", p, u)
	}
}

func TestVerifyGateState_failingExitIsRepair(t *testing.T) {
	mgr, sess, history := verifyGateHarness(t, "go test ./...")
	recordVerify(t, mgr, sess, "go test ./...", 1)
	p, r, u := mgr.Verification.GateState(context.Background(), sess, history)
	if p || !r || u {
		t.Fatalf("one failing exit = (pass %v, repair %v, unverified %v) want (false,true,false)", p, r, u)
	}
}

func TestVerifyGateState_capExhaustedIsUnverified(t *testing.T) {
	mgr, sess, history := verifyGateHarness(t, "go test ./...")
	// Under the cap: still repair.
	recordVerify(t, mgr, sess, "go test ./...", 1)
	recordVerify(t, mgr, sess, "go test ./...", 1)
	if _, r, u := mgr.Verification.GateState(context.Background(), sess, history); !r || u {
		t.Fatalf("under cap = (repair %v, unverified %v) want (true,false)", r, u)
	}
	// At the cap (3 failing attempts): stop steering to repair, admit unverified.
	recordVerify(t, mgr, sess, "go test ./...", 1)
	p, r, u := mgr.Verification.GateState(context.Background(), sess, history)
	if p || r || !u {
		t.Fatalf("at cap = (pass %v, repair %v, unverified %v) want (false,false,true)", p, r, u)
	}
	// A later pass still wins over an exhausted run.
	recordVerify(t, mgr, sess, "go test ./...", 0)
	if p, _, u := mgr.Verification.GateState(context.Background(), sess, history); !p || u {
		t.Fatalf("pass after exhaustion = (pass %v, unverified %v) want (true,false)", p, u)
	}
}

func TestVerifyGateState_declaredCommandMatch(t *testing.T) {
	mgr, sess, history := verifyGateHarness(t, "./task check")
	// A selected command narrows the gate's accepted evidence.
	recordVerify(t, mgr, sess, "go test ./...", 0)
	if p, _, _ := mgr.Verification.GateState(context.Background(), sess, history); p {
		t.Fatal("declared gate must not pass on an unrelated command")
	}
	recordVerify(t, mgr, sess, "./task check", 0)
	if p, _, _ := mgr.Verification.GateState(context.Background(), sess, history); !p {
		t.Fatal("declared command passing should satisfy the gate")
	}
}

func TestVerifyGateState_sourceChangeInvalidatesPass(t *testing.T) {
	mgr, sess, history := verifyGateHarness(t, "./task check")
	recordVerify(t, mgr, sess, "./task check", 0)
	if passed, _, _ := mgr.Verification.GateState(context.Background(), sess, history); !passed {
		t.Fatal("current revision did not accept its verify evidence")
	}
	repochange.Advance(sess.WorkspacePath)
	if passed, repair, unverified := mgr.Verification.GateState(context.Background(), sess, history); passed || !repair || unverified {
		t.Fatalf("stale pass must retain its attempt: pass=%v repair=%v unverified=%v", passed, repair, unverified)
	}
	// Reverification of the new generation restores the gate.
	recordVerify(t, mgr, sess, "./task check", 0)
	if passed, _, _ := mgr.Verification.GateState(context.Background(), sess, history); !passed {
		t.Fatal("new revision did not accept fresh verify evidence")
	}
}

// Launch-only captures carry no terminal evidence.
func TestRecordSourceRunEvidenceIgnoresLaunchOnlyCapture(t *testing.T) {
	for _, producer := range []string{verification.ProducerVerify, verification.ProducerCommand} {
		t.Run(producer, func(t *testing.T) {
			mgr, sess, history := verifyGateHarness(t, "./task check")
			mgr.Verification.RecordSourceRunEvidence(t.Context(), sess.ID, sess, producer, tools.SourceRunCapture{})
			if passed, repair, exhausted := mgr.Verification.GateState(t.Context(), sess, history); passed || repair || exhausted {
				t.Fatalf("launch recorded as terminal evidence: pass=%v repair=%v exhausted=%v", passed, repair, exhausted)
			}
		})
	}
}

func TestVerifyGateState_undeclaredCommandPassSatisfies(t *testing.T) {
	mgr, sess, history := verifyGateHarness(t, "")
	history = workSince(history)
	recordCommand(t, mgr, sess, "./ntp_check.py --json", 0)
	if p, _, u := mgr.Verification.GateState(context.Background(), sess, history); !p || u {
		t.Fatalf("undeclared command pass = (pass %v, unverified %v) want (true,false)", p, u)
	}
}

func TestVerifyGateStateDeclaredAcceptsMatchingCommandPass(t *testing.T) {
	mgr, sess, history := verifyGateHarness(t, "./task check")
	recordCommand(t, mgr, sess, "./task check", 0)
	if p, _, _ := mgr.Verification.GateState(context.Background(), sess, history); !p {
		t.Fatal("declared gate must accept its command through either execution tool")
	}
}

func TestVerifyGateState_commandUnverifiableCountsAsAttempt(t *testing.T) {
	mgr, sess, history := verifyGateHarness(t, "")
	mgr.Verification.RecordSourceRunEvidence(context.Background(), sess.ID, sess, verification.ProducerCommand, tools.SourceRunCapture{
		Command: "./ntp_check.py --json", ExitCode: 1, Verdict: api.SourceVerdictUnverifiable,
		IsCheck: true,
	})
	p, r, u := mgr.Verification.GateState(context.Background(), sess, history)
	if p || !r || u {
		t.Fatalf("unverifiable command = (pass %v, repair %v, unverified %v) want (false,true,false)", p, r, u)
	}
}

func TestWorkflowSourceVerifyPassedReadsCurrentSessionEvidence(t *testing.T) {
	ctx := t.Context()
	memory := store.NewMemory()
	mgr := NewManager(memory, nil, nil, settings.DefaultSessionLimits())
	mgr.SetDataDir(t.TempDir())
	mgr.Verification.SetEvidenceStore(inspector.NewJSONLStore(inspector.DefaultEvidenceDir))
	mgr.Verification.SetRevisionSource(func(_ context.Context, root string) (string, string) {
		return invocation.SourceRevisionForRoot(root)
	})
	sess, err := memory.Create(ctx, api.CreateSessionRequest{ProjectID: "verify-proj"}, "verify-proj")
	testutil.FailErr(t, "create verify session", err)
	sess.WorkspacePath = t.TempDir()
	testutil.FailErr(t, "append user boundary", memory.AppendMessages(ctx, sess.ID, api.Message{
		ID: "user-1", Role: api.MessageRoleUser, Content: "fix it", CreatedAt: time.Now().Add(-time.Minute),
	}))
	recordVerify(t, mgr, sess, "go test ./...", 0)

	passed, err := mgr.Verification.WorkflowSourceVerifyPassed(ctx, sess.ID)
	testutil.FailErr(t, "WorkflowSourceVerifyPassed", err)
	if !passed {
		t.Fatal("current passing source verification was not projected to workflow conditions")
	}
}

func TestSourceRunEvidenceRequiresTerminalVerdict(t *testing.T) {
	for _, outcome := range []string{"", "unknown"} {
		t.Run(outcome, func(t *testing.T) {
			mgr, sess, history := verifyGateHarness(t, "project-check")
			run := statedRun("project-check", 0)
			run.Verdict = outcome
			run.SourceRevision, run.SourceRootDigest = invocation.SourceRevisionForRoot(sess.WorkspacePath)
			mgr.Verification.RecordSourceRunEvidence(t.Context(), sess.ID, sess, verification.ProducerCommand, run)
			records, err := mgr.Verification.ReadSourceRecords(t.Context(), sess)
			testutil.FailErr(t, "read terminal evidence", err)
			if len(records) != 1 || records[0].GateVerdict != string(evidence.GateVerdictUnverifiable) {
				t.Fatalf("missing terminal verdict inferred an outcome: %+v", records)
			}
			if passed, _, _ := mgr.Verification.GateState(t.Context(), sess, history); passed {
				t.Fatal("exit code alone satisfied a workflow gate")
			}
		})
	}
}

func TestCommandCompletionRecordsPromotedCommandEvidence(t *testing.T) {
	ctx := context.Background()
	memory := store.NewMemory()
	mgr := NewManager(memory, nil, nil, settings.DefaultSessionLimits())
	mgr.SetDataDir(t.TempDir())
	evidenceStore := inspector.NewJSONLStore(inspector.DefaultEvidenceDir)
	mgr.Verification.SetEvidenceStore(evidenceStore)
	sess, err := memory.Create(ctx, api.CreateSessionRequest{ProjectID: "verify-proj"}, "verify-proj")
	testutil.FailErr(t, "create completion session", err)

	finishedAt := time.Now().UTC()
	mgr.Processes.HandleCommandCompletion(ctx, bgprocess.Completion{
		Handle: "command-1", SessionID: sess.ID, ProjectID: sess.ProjectID,
		OriginTool: "command", RunID: sess.ID, Mode: bgprocess.JobModeAwaited,
		StartedAt: finishedAt.Add(-time.Minute), FinishedAt: finishedAt,
		TerminationReason: bgprocess.TerminationExited, ExitCode: 0,
		Stages: []hostcmd.StageResult{{Command: "./ntp_check.py --json", ExitCode: &exitZero}},
	})

	records, err := evidenceStore.ReadAll(
		ctx, mgr.HostDataDirFor(sess.ProjectID), sess.ID, verification.VerifySlot, evidence.GateTypeVerify,
	)
	testutil.FailErr(t, "read completion command evidence", err)
	if len(records) != 1 {
		t.Fatalf("completion evidence records = %d want 1", len(records))
	}
	if records[0].TypedGateVerdict() != evidence.GateVerdictPassed {
		t.Fatalf("completion verdict = %q want passed", records[0].TypedGateVerdict())
	}
	if got, _ := records[0].Artifacts["producer"].(string); got != verification.ProducerCommand {
		t.Fatalf("producer = %q", got)
	}
}

func TestCommandCompletionRecordsPromotedVerifyEvidence(t *testing.T) {
	ctx := context.Background()
	memory := store.NewMemory()
	mgr := NewManager(memory, nil, nil, settings.DefaultSessionLimits())
	mgr.SetDataDir(t.TempDir())
	evidenceStore := inspector.NewJSONLStore(inspector.DefaultEvidenceDir)
	mgr.Verification.SetEvidenceStore(evidenceStore)
	sess, err := memory.Create(ctx, api.CreateSessionRequest{ProjectID: "verify-proj"}, "verify-proj")
	testutil.FailErr(t, "create completion session", err)

	finishedAt := time.Now().UTC()
	mgr.Processes.HandleCommandCompletion(ctx, bgprocess.Completion{
		Handle: "command-1", SessionID: sess.ID, ProjectID: sess.ProjectID,
		OriginTool: "verify", RunID: sess.ID, Mode: bgprocess.JobModeAwaited,
		StartedAt: finishedAt.Add(-time.Minute), FinishedAt: finishedAt,
		TerminationReason: bgprocess.TerminationExited, ExitCode: 0,
		Stages: []hostcmd.StageResult{{Command: "./task check-fast", ExitCode: &exitZero}},
	})

	records, err := evidenceStore.ReadAll(
		ctx, mgr.HostDataDirFor(sess.ProjectID), sess.ID, verification.VerifySlot, evidence.GateTypeVerify,
	)
	testutil.FailErr(t, "read completion verify evidence", err)
	if len(records) != 1 {
		t.Fatalf("completion evidence records = %d want 1", len(records))
	}
	if records[0].TypedGateVerdict() != evidence.GateVerdictPassed {
		t.Fatalf("completion verdict = %q want passed", records[0].TypedGateVerdict())
	}
}

func TestConfirmVerifyResult_stampsHostConfirmation(t *testing.T) {
	declaredMgr, sess, _ := verifyGateHarness(t, "./task check")
	const matched = `{"stages":[{"command":"./task check","exit_code":0}],"exit_code":0,"passed":true}`
	if got := declaredMgr.Verification.ConfirmVerifyResult(sess, matched); !strings.Contains(got, `"declared_command_match":true`) {
		t.Fatalf("declared command match should stamp declared_command_match=true, got %s", got)
	}
	const mismatched = `{"stages":[{"command":"echo ok","exit_code":0}],"exit_code":0,"passed":true}`
	if got := declaredMgr.Verification.ConfirmVerifyResult(sess, mismatched); !strings.Contains(got, `"declared_command_match":false`) {
		t.Fatalf("unrelated command must stamp declared_command_match=false, got %s", got)
	}
	if got := declaredMgr.Verification.ConfirmVerifyResult(sess, mismatched); !strings.Contains(got, `"declared_command":"./task check"`) {
		t.Fatalf("declared project must stamp declared_command on result, got %s", got)
	}

	// Declared-but-empty command (the wired undeclared case) — a passing run is never confirmed.
	undeclaredMgr, sess2, _ := verifyGateHarness(t, "")
	undeclaredMgr.Verification.SetVerifyConfig(stubVerifyConfig{cmd: ""})
	if got := undeclaredMgr.Verification.ConfirmVerifyResult(sess2, mismatched); !strings.Contains(got, `"declared_command_match":false`) {
		t.Fatalf("undeclared project must stamp declared_command_match=false, got %s", got)
	}
	// Unparseable content is returned untouched.
	if got := declaredMgr.Verification.ConfirmVerifyResult(sess, "not json"); got != "not json" {
		t.Fatalf("non-JSON content should pass through, got %s", got)
	}
}
