package session

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/pkg/api"
)

// NotifyWorkerCycleTerminal flushes deferred loop wakes when the parent worker cycle is idle.
func (m *Manager) NotifyWorkerCycleTerminal(ctx context.Context, parentID, completingJobID string) {
	if m == nil {
		return
	}
	m.ensureCoordinatorRuntime().CoordinatorLoop().OnWorkerCycleTerminal(ctx, parentID, completingJobID)
	m.reconcileCoordinatorBatchFromLedger(ctx, parentID)
	m.disarmCoordinatorLoopIfBatchTerminal(ctx, parentID)
	m.maybeReconcileSandboxesOnIdle(ctx, parentID)
	m.maybeRunPromotion(ctx, parentID)
}

// NudgeLegFinishedLoopWake queues leg-finished loop wake for tests and delegation outcomes.
func (m *Manager) NudgeLegFinishedLoopWake(ctx context.Context, parentID string, completedAt time.Time, legID string) {
	if m == nil {
		return
	}
	m.nudgeLegFinished(ctx, parentID, completedAt, legID)
}

// RecordWorkerTerminalProofAfterQueueComplete records stored summary status.
func (m *Manager) RecordWorkerTerminalProofAfterQueueComplete(ctx context.Context, parentID, jobID, status string) error {
	if m == nil || m.workflows == nil {
		return nil
	}
	if strings.TrimSpace(jobID) == "" {
		return nil
	}
	if summary := m.workerSummaryStatusForJob(ctx, parentID, jobID); summary != "" {
		status = summary
	}
	return m.workflows.Fanout.RecordWorkerTerminalProof(ctx, parentID, jobID, status)
}

func (m *Manager) workerSummaryStatusForJob(ctx context.Context, parentID, jobID string) string {
	if m == nil {
		return ""
	}
	tags, err := m.WorkerSummaryTags(ctx, parentID)
	if err != nil {
		return ""
	}
	jobID = strings.TrimSpace(jobID)
	for _, tag := range tags {
		if strings.TrimSpace(tag.WorkerID) == jobID {
			return strings.TrimSpace(string(tag.Status))
		}
	}
	return ""
}

// CoordinatorEnvelopeForWorkerCycleTerminal builds terminal worker facts.
func (m *Manager) CoordinatorEnvelopeForWorkerCycleTerminal(ctx context.Context, sessionID, completingJobID string) anchor.Envelope {
	var env anchor.Envelope
	if m == nil {
		return env
	}
	jobID := strings.TrimSpace(completingJobID)
	if jobID == "" {
		// Manifest hooks may omit the completing job.
		jobID = m.workerCycleCompletingJobID(ctx, sessionID)
	}
	if jobID == "" {
		return env
	}
	now := time.Now().UTC()
	env.CompletedAt = &now
	if digest := m.takeWorkerDigest(jobID); digest != "" {
		env.WorkerDigest = digest
	}
	if m.decisions != nil {
		req, ok, err := m.decisions.GetByJob(ctx, jobID)
		if err != nil {
			slog.WarnContext(ctx, "load worker decision for coordinator envelope", "job_id", jobID, "error", err)
		} else if ok {
			env.WithWorkerDecision(req)
		}
	}
	if m.workerQueue != nil {
		if task, ok := m.workerQueue.Get(jobID); ok && task != nil {
			facts := workerBudgetFacts(task, m.workerToolBudgetForTask(ctx, task))
			if facts.Exhausted || facts.Request != nil {
				env.WorkerBudget = &facts
			}
		}
	}
	// Anything the takes above did not claim belongs to a job that is over.
	m.ForgetJob(jobID)
	return env
}

// workerCycleCompletingJobID returns completing_job_id from active-run worker_cycle scaffold vars.
func (m *Manager) workerCycleCompletingJobID(ctx context.Context, sessionID string) string {
	if m == nil || m.loopWorkflowSource == nil {
		return ""
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ""
	}
	run, err := m.loopWorkflowSource.Runs.ActiveBySession(ctx, sessionID)
	if err != nil || run == nil {
		return ""
	}
	vars, err := m.loopWorkflowSource.Runs.GetScaffoldVars(ctx, run.ID)
	if err != nil {
		return ""
	}
	wc, _ := vars["worker_cycle"].(map[string]any)
	if wc == nil {
		return ""
	}
	jobID, _ := wc["completing_job_id"].(string)
	return strings.TrimSpace(jobID)
}

// WorkerTaskByID returns a worker job snapshot for outcome bridging.
func (m *Manager) WorkerTaskByID(jobID string) (WorkerSummaryInput, bool) {
	if m == nil || m.workerQueue == nil {
		return WorkerSummaryInput{}, false
	}
	task, ok := m.workerQueue.Get(jobID)
	if !ok {
		return WorkerSummaryInput{}, false
	}
	return WorkerSummaryInput{
		JobID:           task.ID,
		AgentType:       task.AgentType,
		DelegationID:    task.DelegationID,
		LegID:           task.LegID,
		ParentSessionID: task.ParentSessionID,
		ChildSessionID:  task.ChildSessionID,
		ProjectDir:      task.WorkspacePath,
		ProjectID:       task.ProjectID,
	}, true
}

// ProjectWorkerResult projects a committed result to the parent transcript.
func (m *Manager) ProjectWorkerResult(ctx context.Context, task WorkerSummaryInput, result api.WorkerResult) (string, error) {
	if result.Status == string(api.WorkerStatusCanceled) {
		report := api.WorkerChangeReport{}
		if result.ChangeReport != nil {
			report = *result.ChangeReport
		}
		err := m.AppendWorkerCancellation(ctx, task.ParentSessionID, WorkerCancellationInput{
			JobID: task.JobID, ChildSessionID: task.ChildSessionID, AgentType: task.AgentType,
			Report: report, Result: result, CompletionReport: workercompletion.ReportFromWire(result.CompletionReport),
		})
		return string(api.WorkerSummaryStatusCanceled), err
	}
	task.Summary = result.Summary
	task.Report = workercompletion.ReportFromWire(result.CompletionReport)
	task.Status = result.Status
	task.HintCode = result.HintCode
	task.PolicyFeedback = result.PolicyFeedback
	task.ReportEvaluated = result.Grounding != nil || result.PolicyFeedback != nil
	grounding := api.CitationGrounding{}
	if result.Grounding != nil {
		grounding = *result.Grounding
	}
	task.GroundingOut = &grounding
	return m.AppendWorkerSummary(ctx, task.ParentSessionID, task)
}

// ProjectWorkerFailure projects a terminal failure to the parent transcript.
func (m *Manager) ProjectWorkerFailure(ctx context.Context, task WorkerSummaryInput, failure error) error {
	risk := ""
	if failure != nil {
		risk = strings.TrimSpace(failure.Error())
	}
	report := workercompletion.WorkerCompletionReport{LegStatus: "blocked"}
	if risk != "" {
		report.RemainingRisk = []string{risk}
	}
	task.Report = report
	task.Status = string(api.WorkerSummaryStatusFailed)
	_, err := m.AppendWorkerSummary(ctx, task.ParentSessionID, task)
	return err
}
