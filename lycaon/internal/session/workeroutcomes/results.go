package workeroutcomes

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/pkg/api"
)

type SummaryProjection interface {
	Append(context.Context, string, SummaryInput) (string, error)
	Tags(context.Context, string) ([]api.WorkerSummaryMeta, error)
}
type CancellationProjection interface {
	Append(context.Context, string, CancellationInput) error
}
type TerminalProofs interface {
	RecordWorkerTerminalProof(context.Context, string, string, string) error
}
type WorkerDecisions interface {
	GetByJob(context.Context, string) (api.WorkerDecisionRequest, bool, error)
}
type ActiveWorkflow interface {
	ActiveRun(context.Context, string) (*api.WorkflowRun, error)
	ScaffoldVars(context.Context, string) (map[string]any, error)
}
type SessionLimits interface {
	Effective(context.Context, *api.Session) settings.SessionLimits
}

type Results struct {
	tasks          TaskReader
	summaries      SummaryProjection
	cancellations  CancellationProjection
	proofs         TerminalProofs
	decisions      WorkerDecisions
	workflowSource ActiveWorkflow
	digests        *Digests
	sessions       SessionReader
	limits         SessionLimits
}

type ResultPorts struct {
	Tasks          TaskReader
	Summaries      SummaryProjection
	Cancellations  CancellationProjection
	Proofs         TerminalProofs
	Decisions      WorkerDecisions
	WorkflowSource ActiveWorkflow
	Digests        *Digests
	Sessions       SessionReader
	Limits         SessionLimits
}

func NewResults(ports ResultPorts) *Results {
	return &Results{tasks: ports.Tasks, summaries: ports.Summaries, cancellations: ports.Cancellations, proofs: ports.Proofs, decisions: ports.Decisions, workflowSource: ports.WorkflowSource, digests: ports.Digests, sessions: ports.Sessions, limits: ports.Limits}
}
func (m *Results) SetSummaries(summaries SummaryProjection) { m.summaries = summaries }
func (m *Results) SetWorkers(tasks TaskReader)              { m.tasks = tasks }
func (m *Results) SetProofs(proofs TerminalProofs)          { m.proofs = proofs }
func (m *Results) SetDecisions(decisions WorkerDecisions)   { m.decisions = decisions }
func (m *Results) SetWorkflowSource(source ActiveWorkflow)  { m.workflowSource = source }

// RecordTerminalProof records stored summary status.
func (m *Results) RecordTerminalProof(ctx context.Context, parentID, jobID, status string) error {
	if m == nil || m.proofs == nil {
		return nil
	}
	if strings.TrimSpace(jobID) == "" {
		return nil
	}
	if summary := m.workerSummaryStatusForJob(ctx, parentID, jobID); summary != "" {
		status = summary
	}
	return m.proofs.RecordWorkerTerminalProof(ctx, parentID, jobID, status)
}

func (m *Results) workerSummaryStatusForJob(ctx context.Context, parentID, jobID string) string {
	if m == nil {
		return ""
	}
	tags, err := m.summaries.Tags(ctx, parentID)
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

// EnvelopeForTerminal builds terminal worker facts.
func (m *Results) EnvelopeForTerminal(ctx context.Context, sessionID, completingJobID string) anchor.Envelope {
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
	if digest := m.digests.Take(jobID); digest != "" {
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
	if m.tasks != nil {
		if task, ok := m.tasks.Get(jobID); ok && task != nil {
			facts := BudgetFacts(task, m.BudgetForTask(ctx, task))
			if facts.Exhausted || facts.Request != nil {
				env.WorkerBudget = &facts
			}
		}
	}
	return env
}

// workerCycleCompletingJobID returns completing_job_id from active-run worker_cycle scaffold vars.
func (m *Results) workerCycleCompletingJobID(ctx context.Context, sessionID string) string {
	if m == nil || m.workflowSource == nil {
		return ""
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ""
	}
	run, err := m.workflowSource.ActiveRun(ctx, sessionID)
	if err != nil || run == nil {
		return ""
	}
	vars, err := m.workflowSource.ScaffoldVars(ctx, run.ID)
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

// TaskByID returns a worker job snapshot for outcome bridging.
func (m *Results) TaskByID(jobID string) (SummaryInput, bool) {
	if m == nil || m.tasks == nil {
		return SummaryInput{}, false
	}
	task, ok := m.tasks.Get(jobID)
	if !ok {
		return SummaryInput{}, false
	}
	return SummaryInput{
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

// ProjectResult projects a committed result to the parent transcript.
func (m *Results) ProjectResult(ctx context.Context, task SummaryInput, result api.WorkerResult) (string, error) {
	if result.Status == string(api.WorkerStatusCanceled) {
		report := api.WorkerChangeReport{}
		if result.ChangeReport != nil {
			report = *result.ChangeReport
		}
		err := m.cancellations.Append(ctx, task.ParentSessionID, CancellationInput{
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
	return m.summaries.Append(ctx, task.ParentSessionID, task)
}

// ProjectFailure projects a terminal failure to the parent transcript.
func (m *Results) ProjectFailure(ctx context.Context, task SummaryInput, failure error) error {
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
	_, err := m.summaries.Append(ctx, task.ParentSessionID, task)
	return err
}

// BudgetForTask resolves ceiling bounds from the worker's parent session.
func (m *Results) BudgetForTask(ctx context.Context, task *api.WorkerTask) spawn.WorkerToolBudget {
	budget := spawn.DefaultWorkerToolBudget()
	if m == nil || task == nil || m.sessions == nil || m.limits == nil {
		return budget
	}
	if parent, err := m.sessions.Get(ctx, strings.TrimSpace(task.ParentSessionID)); err == nil && parent != nil {
		budget = m.limits.Effective(ctx, parent).WorkerToolBudget()
	}
	return budget
}

// BudgetRequestOpen reports a live job still carrying an unanswered
// request; a job's finish wake reports any request it ended with.
func (m *Results) BudgetRequestOpen(jobID string) bool {
	jobID = strings.TrimSpace(jobID)
	if m == nil || m.tasks == nil || jobID == "" {
		return true
	}
	task, ok := m.tasks.Get(jobID)
	if !ok || task == nil || task.BudgetRequest == nil {
		return false
	}
	return task.Status == api.WorkerStatusPending || task.Status == api.WorkerStatusRunning
}
