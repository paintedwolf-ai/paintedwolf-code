package verification

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/inspector"
	"github.com/lycaon/lycaon/internal/project"
	sessiontree "github.com/lycaon/lycaon/internal/session/tree"
	"github.com/lycaon/lycaon/internal/session/workercontext"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// VerifyConfigResolver supplies a project's declared verify command.
type VerifyConfigResolver interface {
	VerifyTestCommand(projectDir string) string
}

// VerifySlot groups test evidence for a run.
const VerifySlot = "tests"

// SetEvidenceStore wires the run-keyed inspector evidence store for verify gating.
func (m *Service) SetEvidenceStore(s inspector.EvidenceStore) {
	if m != nil {
		m.evidenceStore = s
	}
}

// SetVerifyConfig supplies the project's selected test command.
func (m *Service) SetVerifyConfig(r VerifyConfigResolver) {
	if m != nil {
		m.verifyConfig = r
	}
}

// ConfirmVerifyResult compares a result with the declared command.
func (m *Service) ConfirmVerifyResult(sess *api.Session, content string) string {
	if m == nil || m.verifyConfig == nil || sess == nil {
		return content
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(content)), &obj); err != nil {
		return content
	}
	cmd := verifyCommandFromResult(obj)
	declaredRaw := strings.TrimSpace(m.verifyConfig.VerifyTestCommand(m.workspace.SettingsPath(context.Background(), sess)))
	obj["declared_command_match"] = declaredRaw != "" && commandsurface.SameCommandLine(cmd, declaredRaw)
	if declaredRaw != "" {
		obj["declared_command"] = declaredRaw
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return content
	}
	return string(out)
}

func verifyCommandFromResult(obj map[string]any) string {
	if cmd, _ := obj["command"].(string); strings.TrimSpace(cmd) != "" {
		return strings.TrimSpace(cmd)
	}
	raw, err := json.Marshal(obj["stages"])
	if err != nil {
		return ""
	}
	var stages []hostcmd.StageResult
	if err := json.Unmarshal(raw, &stages); err != nil {
		return ""
	}
	return hostcmd.CommandLine(stages)
}

func (m *Service) RecordSourceRunEvidence(
	ctx context.Context,
	sessionID string,
	sess *api.Session,
	tool string,
	run tools.SourceRunCapture,
) {
	if m == nil || m.evidenceStore == nil || sess == nil {
		return
	}
	if tool != ProducerVerify && tool != ProducerCommand {
		return
	}
	if strings.TrimSpace(run.Command) == "" {
		return
	}
	runID := sessiontree.RootID(ctx, m.store, sessionID)
	if strings.TrimSpace(runID) == "" {
		runID = sessionID
	}
	m.RecordSourceRunTerminal(ctx, sess, runID, tool, run, time.Now().UTC())
}

const (
	ProducerVerify  = "verify"
	ProducerCommand = "command"
)

func (m *Service) RecordSourceRunTerminal(
	ctx context.Context,
	sess *api.Session,
	runID, producer string,
	run tools.SourceRunCapture,
	recordedAt time.Time,
) {
	if m == nil || m.evidenceStore == nil || sess == nil {
		return
	}
	command := strings.TrimSpace(run.Command)
	if command == "" {
		return
	}
	if producer != ProducerVerify && producer != ProducerCommand {
		return
	}
	if strings.TrimSpace(runID) == "" {
		runID = sess.ID
	}
	// Evidence persistence screens literal credentials independently of the transcript.
	command = m.transcript.ScreenText(ctx, command)
	rec := evidence.Record{
		GateType:    string(evidence.GateTypeVerify),
		RunID:       runID,
		Slot:        VerifySlot,
		GateVerdict: string(verifyVerdict(run.Verdict)),
		Artifacts: map[string]any{
			"check_id":   run.CheckID,
			"session_id": sess.ID,
			"is_check":   run.IsCheck,
			"command":    command,
			"exit_code":  run.ExitCode,
			"producer":   producer,
			"cwd":        run.Cwd,
		},
		RecordedAt: recordedAt.UTC().Format(time.RFC3339Nano),
	}
	rec.Artifacts["source_revision"] = run.SourceRevision
	rec.Artifacts["source_root_digest"] = run.SourceRootDigest
	root := m.EvidenceRootFor(sess)
	if root != "" {
		if err := m.evidenceStore.Append(ctx, root, rec); err != nil {
			slog.ErrorContext(ctx, "Record verification evidence", "session_id", sess.ID, "error", err)
		}
	}
}

// SourceVerifyCommand resolves the project check independently of the execution tool.
func (m *Service) SourceVerifyCommand(ctx context.Context, projectDir string) string {
	if m == nil || m.verifyConfig == nil {
		return ""
	}
	return strings.TrimSpace(m.verifyConfig.VerifyTestCommand(projectDir))
}

func (m *Service) SourceEvidenceRoot(ctx context.Context, sess *api.Session) string {
	if m == nil || sess == nil {
		return ""
	}
	if m.workerQueue != nil && sess.IsWorkerChild() {
		if task, ok := m.workerQueue.Get(workercontext.Job(ctx)); ok && task != nil {
			if root := strings.TrimSpace(task.WorkspaceRoot); root != "" {
				return root
			}
		}
	}
	root, _ := m.workspace.ActivePath(ctx, sess)
	if strings.TrimSpace(root) == "" {
		root = sess.WorkspacePath
	}
	return strings.TrimSpace(root)
}

// EvidenceRootFor returns the host data dir when the session has a project_id.
func (m *Service) EvidenceRootFor(sess *api.Session) string {
	if sess == nil {
		return ""
	}
	if root := project.HostDataDir(m.dataDir, sess.ProjectID); root != "" {
		_, _ = project.EnsureHostDataDir(m.dataDir, sess.ProjectID)
		return root
	}
	return ""
}

func BackgroundOutcome(completion bgprocess.Completion) string {
	if completion.BoundaryRefusal != "" {
		return api.SourceVerdictUnverifiable
	}
	if completion.TerminationReason == bgprocess.TerminationStopped || completion.TerminationReason == bgprocess.TerminationTimedOut {
		return api.SourceVerdictFailed
	}
	if completion.ExitCode == 0 && completion.Failure == nil {
		return api.SourceVerdictPassed
	}
	return api.SourceVerdictFailed
}

func verifyVerdict(outcome string) evidence.GateVerdict {
	switch outcome {
	case api.SourceVerdictUnverifiable:
		return evidence.GateVerdictUnverifiable
	case api.SourceVerdictPassed:
		return evidence.GateVerdictPassed
	case api.SourceVerdictFailed:
		return evidence.GateVerdictFailed
	}
	return evidence.GateVerdictUnverifiable
}

// MaxAttemptsPerRun bounds repair attempts.
const MaxAttemptsPerRun = 3

func (m *Service) WorkflowGateState(
	ctx context.Context,
	sess *api.Session,
	history []api.Message,
) (required, passed, repair, unverified bool) {
	if sess == nil {
		return false, false, false, false
	}
	required = m.RequiresPhaseVerify(ctx, sess.ID)
	if !required {
		return false, false, false, false
	}
	passed, repair, unverified = m.GateState(ctx, sess, history)
	return true, passed, repair, unverified
}

// GateState evaluates source-run evidence for the current run.
func (m *Service) GateState(ctx context.Context, sess *api.Session, history []api.Message) (passed, repair, unverified bool) {
	if m == nil || m.evidenceStore == nil || sess == nil {
		return false, false, false
	}
	runID := strings.TrimSpace(sessiontree.RootID(ctx, m.store, sess.ID))
	if runID == "" {
		return false, false, false
	}
	declared := ""
	if m.verifyConfig != nil {
		declared = strings.TrimSpace(m.verifyConfig.VerifyTestCommand(m.workspace.SettingsPath(ctx, sess)))
	}
	bound := boundaryTime(history)
	currentRevision, currentRootDigest := m.Revision(ctx, m.SourceEvidenceRoot(ctx, sess))
	records, err := m.evidenceStore.ReadAll(ctx, m.EvidenceRootFor(sess), runID, VerifySlot, evidence.GateTypeVerify)
	if err != nil {
		return false, false, false
	}
	attempts := 0
	seen := make(map[string]bool)
	latest := make(map[string]bool)
	for _, rec := range records {
		if rec.TypedGateType() != evidence.GateTypeVerify || rec.GateAt().Before(bound) {
			continue
		}
		producer, _ := rec.Artifacts["producer"].(string)
		cmd, _ := rec.Artifacts["command"].(string)
		if producer != ProducerVerify && producer != ProducerCommand {
			continue
		}
		isCheck, _ := rec.Artifacts["is_check"].(bool)
		if !isCheck && !(declared != "" && commandsurface.SameCommandLine(cmd, declared)) {
			continue
		}
		checkID, _ := rec.Artifacts["check_id"].(string)
		if checkID != "" && seen[checkID] {
			continue
		}
		seen[checkID] = true
		if declared != "" {
			cwd, _ := rec.Artifacts["cwd"].(string)
			if !commandsurface.SameCommandLine(cmd, declared) || cwd != "." {
				continue
			}
		}
		attempts++
		revision, _ := rec.Artifacts["source_revision"].(string)
		rootDigest, _ := rec.Artifacts["source_root_digest"].(string)
		if currentRevision == "" || currentRootDigest == "" || revision != currentRevision || rootDigest != currentRootDigest {
			continue
		}
		cwd, _ := rec.Artifacts["cwd"].(string)
		latest[commandsurface.CheckKey(cmd, cwd)] = rec.GateVerdict == string(evidence.GateVerdictPassed)
	}
	passed = len(latest) > 0
	for _, success := range latest {
		passed = passed && success
	}
	switch {
	case passed:
		return true, false, false
	case attempts >= MaxAttemptsPerRun:
		return false, false, true
	case attempts > 0:
		return false, true, false
	default:
		return false, false, false
	}
}

func (m *Service) Revision(ctx context.Context, root string) (string, string) {
	if m.verificationSource != nil {
		return m.verificationSource(ctx, root)
	}
	return sourceledger.VerificationState(ctx, m.sourceObservations, root)
}

// boundaryTime is the current user-intent boundary; older evidence does not count.
func boundaryTime(history []api.Message) time.Time {
	since := api.UserIntentBoundary(history)
	if since >= 0 && since < len(history) {
		return history[since].CreatedAt
	}
	return time.Time{}
}
