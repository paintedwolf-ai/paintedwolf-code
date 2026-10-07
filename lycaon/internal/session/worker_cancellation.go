package session

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/workercloseout"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/pkg/api"
)

// StopWorkerRuntime releases only one worker's resources, including parked processes.
func (m *Manager) StopWorkerRuntime(ctx context.Context, childID string) error {
	child, err := m.store.Get(ctx, childID)
	if err != nil {
		return err
	}
	if !child.IsWorkerChild() {
		return fmt.Errorf("worker child session required")
	}
	m.CancelInFlightPrompt(child.ID)
	member := store.SessionTreeMember{ID: child.ID, ProjectID: child.ProjectID}
	if err := errors.Join(m.stopOneSessionRuntime(ctx, member, "", nil)...); err != nil {
		return err
	}
	m.FinishWorkerGracefulCancel(child.ID)
	return m.store.SetSessionStatus(ctx, child.ID, api.SessionStatusIdle)
}

// WorkerCancellationInput is the parent cancellation envelope.
type WorkerCancellationInput struct {
	JobID            string
	AgentType        string
	ChildSessionID   string
	Reason           string
	Report           api.WorkerChangeReport
	CompletionReport workercompletion.WorkerCompletionReport
	Result           api.WorkerResult
}

// AppendWorkerCancellation records a canceled worker leg on the coordinator transcript.
func (m *Manager) AppendWorkerCancellation(ctx context.Context, parentID string, in WorkerCancellationInput) error {
	if m == nil {
		return fmt.Errorf("session manager unavailable")
	}
	parentID = strings.TrimSpace(parentID)
	if parentID == "" {
		return fmt.Errorf("parent session id required")
	}
	proof := workercompletion.WorkerCompletionProof{
		ChangedPaths:   append([]string(nil), in.Report.ChangedPaths...),
		WorkspaceDirty: in.Report.WorkspaceDirty,
		MutationTools:  append([]string(nil), in.Report.MutationTools...),
		SurveyTools:    append([]string(nil), in.Report.SurveyTools...),
		ReceiptCount:   in.Report.ReceiptCount,
	}
	report := workercompletion.WorkerCompletionReport{
		LegStatus:     "partial",
		FilesModified: append([]string(nil), proof.ChangedPaths...),
		ObjectivesMet: []string{"Worker leg canceled"},
		RemainingRisk: []string{strings.TrimSpace(in.Reason)},
		Brief:         strings.TrimSpace(in.Result.Summary),
	}
	if agentReport := in.CompletionReport; strings.TrimSpace(agentReport.Brief) != "" || len(agentReport.ObjectivesMet) > 0 {
		report = agentReport
		if report.DeclaredLegStatus == "" {
			report.DeclaredLegStatus = report.LegStatus
		}
		report.LegStatus = "partial"
		report.FilesModified = append([]string(nil), proof.ChangedPaths...)
		if strings.TrimSpace(in.Reason) != "" {
			report.RemainingRisk = workercloseout.AppendRemainingRisk(report.RemainingRisk, strings.TrimSpace(in.Reason))
		}
	}
	report.Normalize()
	body := strings.TrimSpace(in.Reason)
	summary := strings.TrimSpace(in.Result.Summary)
	if feedback := in.Result.PolicyFeedback; feedback != nil {
		if feedback.Code != in.Result.HintCode {
			return fmt.Errorf("canceled worker feedback code does not match hint_code")
		}
		rendered, err := guidance.RenderPolicyCopy(ctx, feedback.Code, feedback.Effect, feedback.Copy)
		if err != nil {
			return fmt.Errorf("render canceled worker feedback: %w", err)
		}
		summary = strings.TrimSpace(summary + "\n\n" + rendered)
	}
	envelope := FormatWorkerCompletionEnvelope(WorkerCompletionEnvelope{
		JobID:          in.JobID,
		ChildSessionID: in.ChildSessionID,
		AgentType:      in.AgentType,
		State:          string(api.WorkerSummaryStatusCanceled),
		Summary:        summary,
		Body:           body,
		HintCode:       in.Result.HintCode,
		Proof:          proof,
		Report:         report,
	})
	ws := &api.WorkerSummaryMeta{
		WorkerID:       in.JobID,
		ChildSessionID: in.ChildSessionID,
		AgentType:      in.AgentType,
		Status:         api.WorkerSummaryStatusCanceled,
		Grounding:      in.Result.Grounding,
		Envelope:       envelope,
	}
	if err := m.ProjectWorkerCard(ctx, parentID, in.JobID, ws); err != nil {
		return err
	}
	if m.grounding != nil {
		m.grounding.Reset(parentID)
	}
	return nil
}
