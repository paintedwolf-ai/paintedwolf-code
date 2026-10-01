package session

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/pkg/api"
)

func turnCalledUpdateProgress(turnTools []string) bool {
	for _, tool := range turnTools {
		if strings.TrimSpace(tool) == "update_progress" {
			return true
		}
	}
	return false
}

// afterPrompt runs post-turn hooks. Workers skip phase-exit, progress, and grounding.
func (m *Manager) afterPrompt(ctx context.Context, sessionID, profileID string, turnTools []string) error {
	isWorker := false
	if m.store != nil {
		if sess, err := m.store.Get(ctx, strings.TrimSpace(sessionID)); err == nil &&
			sess != nil && sess.IsWorkerChild() {
			isWorker = true
		}
	}
	if isWorker {
		return nil
	}
	if m.maybeNudgeWorkflowPhaseExit(ctx, sessionID, profileID, turnTools) {
		return nil
	}
	m.maybeNudgeProgressMissing(ctx, sessionID, turnTools)
	if m.grounding == nil {
		return nil
	}
	if err := m.grounding.AfterPrompt(ctx, sessionID, turnTools); err != nil {
		var nudge *ErrGroundingNudge
		if errors.As(err, &nudge) {
			m.queueCoordinatorGuidanceAdvisories(ctx, sessionID, nudge.Nudges())
			return nil
		}
		if errors.Is(err, ErrGroundingEscalated) {
			return err
		}
	}
	return nil
}

// maybeNudgeProgressMissing nudges the coordinator to author a progress checklist via
// update_progress when it attempts progress-gated tools without a checklist.
func (m *Manager) maybeNudgeProgressMissing(ctx context.Context, sessionID string, turnTools []string) {
	if m == nil || m.progress == nil {
		return
	}
	if m.store != nil {
		if sess, err := m.store.Get(ctx, strings.TrimSpace(sessionID)); err == nil && sess != nil {
			if sess.Posture == api.SessionPostureSpec || sess.IsWorkerChild() {
				return
			}
		}
	}
	if turnCalledUpdateProgress(turnTools) || !progress.TurnHasProgressGatedTool(turnTools) {
		return
	}
	if !progress.ProgressMissing(m.progress.Get(ctx, sessionID)) {
		return
	}
	m.Emit(ctx, sessionID, anchor.ProgressMissing, anchor.Envelope{Vars: map[string]any{
		"tool": progress.FirstProgressGatedTool(turnTools),
	}})
}

// ArmProgressClosure latches the checklist after a worker settles its
// deliverable. Only a successful finish arms it: a partial, failed, or held
// leg has closed nothing, and its resume is the same deliverable continuing.
// The first baseline holds until the checklist changes; later successes join
// the list of finishes the reconciliation must account for.
func (m *Manager) ArmProgressClosure(ctx context.Context, rootID, jobID string) {
	if m == nil || m.progress == nil {
		return
	}
	rootID = strings.TrimSpace(rootID)
	if rootID == "" {
		return
	}
	content := m.progress.Get(ctx, rootID)
	done, pending, na := progress.CloseCounts(content)
	if pending == 0 {
		m.progressClosureExpect.Delete(rootID)
		return
	}
	baseline, armed := m.progressClosureExpect.Load(rootID)
	if !armed {
		baseline = guard.ProgressClosureBaseline{Closed: done + na, Content: content}
	}
	if entry := m.settledWorkEntry(jobID); entry != "" && !slices.Contains(baseline.Settled, entry) {
		baseline.Settled = append(slices.Clone(baseline.Settled), entry)
	}
	m.progressClosureExpect.Store(rootID, baseline)
}

// settledWorkEntry names a finished job for the reconciliation reject.
func (m *Manager) settledWorkEntry(jobID string) string {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" || m.workerQueue == nil {
		return jobID
	}
	task, ok := m.workerQueue.Get(jobID)
	if !ok || task == nil {
		return jobID
	}
	entry := task.AgentType + " `" + jobID + "`"
	if brief := strings.TrimSpace(task.Brief); brief != "" {
		entry += ": " + brief
	}
	return entry
}

// progressClosureLatch returns the armed checklist baseline for rootID.
func (m *Manager) progressClosureLatch(rootID string) (baseline guard.ProgressClosureBaseline, armed bool) {
	if m == nil {
		return guard.ProgressClosureBaseline{}, false
	}
	rootID = strings.TrimSpace(rootID)
	if rootID == "" {
		return guard.ProgressClosureBaseline{}, false
	}
	exp, ok := m.progressClosureExpect.Load(rootID)
	if !ok {
		return guard.ProgressClosureBaseline{}, false
	}
	return exp, true
}

func (m *Manager) clearProgressClosureIfSatisfied(rootID, progressContent string, baseline guard.ProgressClosureBaseline) {
	if m == nil {
		return
	}
	rootID = strings.TrimSpace(rootID)
	if rootID == "" {
		return
	}
	done, pending, na := progress.CloseCounts(progressContent)
	if pending == 0 || done+na > baseline.Closed || progressContent != baseline.Content {
		m.progressClosureExpect.Delete(rootID)
	}
}

// Unchanged checklist writes keep the reconciliation latch armed.
func (m *Manager) MaybeClearProgressClosureAfterWrite(ctx context.Context, rootID string) {
	if m == nil || m.progress == nil {
		return
	}
	rootID = strings.TrimSpace(rootID)
	if rootID == "" {
		return
	}
	baseline, armed := m.progressClosureLatch(rootID)
	if !armed {
		return
	}
	m.clearProgressClosureIfSatisfied(rootID, m.progress.Get(ctx, rootID), baseline)
}

// queueChecklistReconcileNudge marks open progress for the next turn.
func (m *Manager) queueChecklistReconcileNudge(ctx context.Context, rootID string) {
	if m == nil || m.progress == nil {
		return
	}
	rootID = strings.TrimSpace(rootID)
	if rootID == "" {
		return
	}
	content := m.progress.Get(ctx, rootID)
	if !progress.HasOpenSteps(content) {
		return
	}
	_, pending, _ := progress.CloseCounts(content)
	m.Emit(ctx, rootID, anchor.ProgressStale, anchor.Envelope{Vars: map[string]any{"pending": pending}})
}
