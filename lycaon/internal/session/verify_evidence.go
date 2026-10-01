package session

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
	"github.com/lycaon/lycaon/internal/session/workercontext"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// VerifyConfigResolver supplies a project's declared verify command.
type VerifyConfigResolver interface {
	VerifyTestCommand(projectDir string) string
}

// verifySlot groups test evidence for a run.
const verifySlot = "tests"

// SetEvidenceStore wires the run-keyed inspector evidence store for verify gating.
func (m *Manager) SetEvidenceStore(s inspector.EvidenceStore) {
	if m != nil {
		m.evidenceStore = s
	}
}

// SetVerifyConfig supplies the project's selected test command.
func (m *Manager) SetVerifyConfig(r VerifyConfigResolver) {
	if m != nil {
		m.verifyConfig = r
	}
}

// confirmVerifyResult compares a result with the declared command.
func (m *Manager) confirmVerifyResult(sess *api.Session, content string) string {
	if m == nil || m.verifyConfig == nil || sess == nil {
		return content
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(content)), &obj); err != nil {
		return content
	}
	cmd := verifyCommandFromResult(obj)
	declaredRaw := strings.TrimSpace(m.verifyConfig.VerifyTestCommand(m.overlayProjectDir(context.Background(), sess)))
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

func (m *Manager) recordSourceRunEvidence(
	ctx context.Context,
	sessionID string,
	sess *api.Session,
	tool string,
	run tools.SourceRunCapture,
) {
	if m == nil || m.evidenceStore == nil || sess == nil {
		return
	}
	if tool != sourceRunProducerVerify && tool != sourceRunProducerCommand {
		return
	}
	if strings.TrimSpace(run.Command) == "" {
		return
	}
	runID := RootSessionID(ctx, m.store, sessionID)
	if strings.TrimSpace(runID) == "" {
		runID = sessionID
	}
	m.recordSourceRunTerminal(ctx, sess, runID, tool, run, time.Now().UTC())
}

const (
	sourceRunProducerVerify  = "verify"
	sourceRunProducerCommand = "command"
)

func (m *Manager) recordSourceRunTerminal(
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
	if producer != sourceRunProducerVerify && producer != sourceRunProducerCommand {
		return
	}
	if strings.TrimSpace(runID) == "" {
		runID = sess.ID
	}
	// Evidence persistence screens literal credentials independently of the transcript.
	command = m.screenTextForStore(ctx, command)
	rec := evidence.Record{
		GateType:    string(evidence.GateTypeVerify),
		RunID:       runID,
		Slot:        verifySlot,
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
	root := m.evidenceRootFor(sess)
	if root != "" {
		if err := m.evidenceStore.Append(ctx, root, rec); err != nil {
			slog.ErrorContext(ctx, "Record verification evidence", "session_id", sess.ID, "error", err)
		}
	}
}

// SourceVerifyCommand resolves the project check independently of the execution tool.
func (m *Manager) SourceVerifyCommand(ctx context.Context, projectDir string) string {
	if m == nil || m.verifyConfig == nil {
		return ""
	}
	return strings.TrimSpace(m.verifyConfig.VerifyTestCommand(projectDir))
}

func (m *Manager) sourceEvidenceRoot(ctx context.Context, sess *api.Session) string {
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
	root, _ := m.sessionActiveRootPath(ctx, sess)
	if strings.TrimSpace(root) == "" {
		root = sess.WorkspacePath
	}
	return strings.TrimSpace(root)
}

// evidenceRootFor returns the host data dir when the session has a project_id.
func (m *Manager) evidenceRootFor(sess *api.Session) string {
	if sess == nil {
		return ""
	}
	if root := m.HostDataDirFor(sess.ProjectID); root != "" {
		_, _ = project.EnsureHostDataDir(m.dataDir, sess.ProjectID)
		return root
	}
	return ""
}

func backgroundVerifyOutcome(completion bgprocess.Completion) string {
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

// maxVerifyAttemptsPerRun bounds repair attempts.
const maxVerifyAttemptsPerRun = 3

func (m *Manager) workflowVerifyGateState(
	ctx context.Context,
	sess *api.Session,
	history []api.Message,
) (required, passed, repair, unverified bool) {
	if sess == nil {
		return false, false, false, false
	}
	required = m.activePhaseRequiresVerify(ctx, sess.ID)
	if !required {
		return false, false, false, false
	}
	passed, repair, unverified = m.verifyGateState(ctx, sess, history)
	return true, passed, repair, unverified
}

// verifyGateState evaluates source-run evidence for the current run.
func (m *Manager) verifyGateState(ctx context.Context, sess *api.Session, history []api.Message) (passed, repair, unverified bool) {
	if m == nil || m.evidenceStore == nil || sess == nil {
		return false, false, false
	}
	runID := strings.TrimSpace(RootSessionID(ctx, m.store, sess.ID))
	if runID == "" {
		return false, false, false
	}
	declared := ""
	if m.verifyConfig != nil {
		declared = strings.TrimSpace(m.verifyConfig.VerifyTestCommand(m.overlayProjectDir(ctx, sess)))
	}
	bound := boundaryTime(history)
	currentRevision, currentRootDigest := m.verificationRevision(ctx, m.sourceEvidenceRoot(ctx, sess))
	records, err := m.evidenceStore.ReadAll(ctx, m.evidenceRootFor(sess), runID, verifySlot, evidence.GateTypeVerify)
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
		if producer != sourceRunProducerVerify && producer != sourceRunProducerCommand {
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
	case attempts >= maxVerifyAttemptsPerRun:
		return false, false, true
	case attempts > 0:
		return false, true, false
	default:
		return false, false, false
	}
}

func (m *Manager) verificationRevision(ctx context.Context, root string) (string, string) {
	if m.verificationSource != nil {
		return m.verificationSource(ctx, root)
	}
	return sourceledger.VerificationState(ctx, m.sourceLedger, root)
}

// boundaryTime is the current user-intent boundary; older evidence does not count.
func boundaryTime(history []api.Message) time.Time {
	since := api.UserIntentBoundary(history)
	if since >= 0 && since < len(history) {
		return history[since].CreatedAt
	}
	return time.Time{}
}
