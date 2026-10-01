package session

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/runeclamp"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// nudgeProcessWait wakes only the owning session's matching process subscription.
func (m *Manager) nudgeProcessWait(ctx context.Context, sessionID, handle string, wake anchor.ID, env anchor.Envelope) bool {
	if m == nil || strings.TrimSpace(sessionID) == "" {
		return false
	}
	loop := m.ensureCoordinatorRuntime().CoordinatorLoop()
	if !loop.SessionSleepingOnProcess(sessionID, handle) {
		return false
	}
	if wake == anchor.ProcessRefused {
		loop.NudgeProcessRefused(ctx, sessionID, handle, env)
	} else {
		loop.NudgeProcessFinished(ctx, sessionID, handle, env)
	}
	return true
}

// HandleCommandCompletion records a settled command or verify and wakes a matching wait.
func (m *Manager) HandleCommandCompletion(ctx context.Context, completion bgprocess.Completion) {
	defer completion.IndexWatch.Release()
	if m == nil || strings.TrimSpace(completion.SessionID) == "" {
		return
	}
	sess, err := m.store.Get(ctx, completion.SessionID)
	if err == nil && sess != nil && len(completion.Stages) > 0 &&
		(completion.OriginTool == sourceRunProducerVerify || completion.OriginTool == sourceRunProducerCommand) {
		m.recordSourceRunTerminal(ctx, sess, completion.RunID, completion.OriginTool, tools.SourceRunCapture{
			CheckID: completion.ToolCallID, IsCheck: completion.IsCheck,
			Command: hostcmd.CommandLine(completion.Stages), ExitCode: completion.ExitCode,
			Verdict: backgroundVerifyOutcome(completion), Cwd: completion.Cwd,
			SourceRevision: completion.SourceRevision, SourceRootDigest: completion.SourceRootDigest,
		}, completion.FinishedAt)
	}
	digest := commandCompletionDigest(completion)
	if sess != nil {
		digest = m.withPostToolGuidance(ctx, sess, completion.OriginTool, digest, guidance.ToolResultFacts{
			Confine: completion.Observation, IndexWatch: completion.IndexWatch,
		})
	}
	m.processReports.publish(completion.SessionID, completion.Handle, digest)
	env := anchor.Envelope{CommandCompletionDigest: digest}
	if m.nudgeProcessWait(ctx, completion.SessionID, completion.Handle, anchor.ProcessFinished, env) {
		return
	}
	if sess == nil || !sess.IsWorkerChild() {
		m.Emit(ctx, completion.SessionID, anchor.ProcessFinished, env)
	}
}

// HandleCommandRefusal reports unshown refusals and wakes a matching process wait.
func (m *Manager) HandleCommandRefusal(ctx context.Context, notice bgprocess.RefusalNotice) {
	if m == nil || strings.TrimSpace(notice.SessionID) == "" {
		return
	}
	sess, err := m.store.Get(ctx, notice.SessionID)
	if err != nil || sess == nil {
		return
	}
	digest := m.withPostToolGuidance(ctx, sess, notice.OriginTool, commandRefusalDigest(notice),
		guidance.ToolResultFacts{Confine: notice.Observation})
	env := anchor.Envelope{CommandRefusalDigest: digest}
	if m.nudgeProcessWait(ctx, notice.SessionID, notice.Handle, anchor.ProcessRefused, env) {
		return
	}
	if !sess.IsWorkerChild() {
		m.Emit(ctx, notice.SessionID, anchor.ProcessRefused, env)
	}
}

// withPostToolGuidance appends the policy guidance the observation raises for
// the job's own tool.
func (m *Manager) withPostToolGuidance(ctx context.Context, sess *api.Session, tool, digest string, facts guidance.ToolResultFacts) string {
	out, _ := m.appendPostToolGuidance(ctx, sess, tool, nil, digest, 0, facts)
	return out
}

func commandCompletionDigest(completion bgprocess.Completion) string {
	const (
		maxTailBytes    = 4096
		maxCommandBytes = 1024
	)
	tail := truncateUTF8Tail(strings.TrimSpace(completion.Tail), maxTailBytes)
	finishedAt := completion.FinishedAt.UTC().Format(time.RFC3339Nano)
	digest := fmt.Sprintf(
		"handle=%s tool=%s mode=%s termination=%s exit_code=%d elapsed=%s finished_at=%s",
		completion.Handle, completion.OriginTool, completion.Mode,
		completion.TerminationReason, completion.ExitCode,
		completion.Elapsed().Round(time.Millisecond), finishedAt,
	)
	if len(completion.Stages) > 0 {
		digest += "\ncommand=" + truncateUTF8Head(completion.Stages[0].Command, maxCommandBytes)
	}
	if completion.Failure != nil {
		digest += "\nexec_failure=" + string(completion.Failure.Kind) + ": " + truncateUTF8Head(completion.Failure.Detail, maxCommandBytes)
	}
	if completion.BoundaryRefusal != "" {
		digest += "\nboundary_refusal=" + completion.BoundaryRefusal
	}
	if len(completion.GuidanceCodes) > 0 {
		digest += "\ncodes=" + strings.Join(completion.GuidanceCodes, ",")
	}
	digest += sandboxRefusalLines(completion.Observation)
	if tail != "" {
		digest += "\ntail:\n" + tail
	}
	return digest
}

func commandRefusalDigest(notice bgprocess.RefusalNotice) string {
	const maxCommandBytes = 1024
	digest := fmt.Sprintf("handle=%s tool=%s mode=%s state=running elapsed=%s unshown_refusals=%d",
		notice.Handle, notice.OriginTool, notice.Mode,
		time.Since(notice.StartedAt).Round(time.Millisecond), notice.Unshown)
	if len(notice.Stages) > 0 {
		digest += "\ncommand=" + truncateUTF8Head(notice.Stages[0].Command, maxCommandBytes)
	}
	return digest + sandboxRefusalLines(notice.Observation)
}

// sandboxRefusalLines includes recovery options and reporting gaps.
func sandboxRefusalLines(obs confine.Observation) string {
	refusals := obs.Refusals
	if len(refusals.Refusals) == 0 && refusals.Witness == confine.WitnessKernel {
		return ""
	}
	var b strings.Builder
	if len(refusals.Refusals) > 0 {
		b.WriteString("\nsandbox_refusals:")
		for _, r := range refusals.Refusals {
			fmt.Fprintf(&b, "\n- %s count=%d recovery=%s", r.Display(), r.Count, r.Recovery)
			if r.Grant != "" {
				b.WriteString(" grant=" + r.Grant)
			}
		}
		if refusals.Omitted > 0 {
			fmt.Fprintf(&b, "\nsandbox_refusals_omitted=%d", refusals.Omitted)
		}
	}
	if refusals.Witness != "" && refusals.Witness != confine.WitnessKernel {
		b.WriteString("\nsandbox_refusal_witness=" + string(refusals.Witness))
	}
	return b.String()
}

func truncateUTF8Head(value string, maxBytes int) string {
	return runeclamp.ClampBytes(strings.ToValidUTF8(value, "�"), maxBytes)
}

func truncateUTF8Tail(value string, maxBytes int) string {
	return runeclamp.ClampBytesTail(strings.ToValidUTF8(value, "�"), maxBytes)
}
