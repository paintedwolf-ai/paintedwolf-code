package workeroutcomes

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/workercloseout"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/pkg/api"
)

type CancellationSessions interface {
	Get(context.Context, string) (*api.Session, error)
	SetSessionStatus(context.Context, string, api.SessionStatus) error
}

type ExecutionCancel interface{ Cancel(string) }
type RuntimeStop interface {
	StopRuntime(context.Context, store.SessionTreeMember, string) error
}
type GracefulFinish interface{ Finish(string) }
type ParentCards interface {
	Project(context.Context, string, string, *api.WorkerSummaryMeta) error
}
type GroundingReset interface{ Reset(string) }

type Cancellations struct {
	sessions  CancellationSessions
	execution ExecutionCancel
	stops     RuntimeStop
	graceful  GracefulFinish
	cards     ParentCards
	grounding GroundingReset
}

type CancellationPorts struct {
	Sessions  CancellationSessions
	Execution ExecutionCancel
	Stops     RuntimeStop
	Graceful  GracefulFinish
	Cards     ParentCards
	Grounding GroundingReset
}

func NewCancellations(ports CancellationPorts) *Cancellations {
	return &Cancellations{sessions: ports.Sessions, execution: ports.Execution, stops: ports.Stops, graceful: ports.Graceful, cards: ports.Cards, grounding: ports.Grounding}
}

// StopRuntime releases only one worker's resources, including parked processes.
func (m *Cancellations) StopRuntime(ctx context.Context, childID string) error {
	child, err := m.sessions.Get(ctx, childID)
	if err != nil {
		return err
	}
	if child == nil || !child.IsWorkerChild() {
		return fmt.Errorf("worker child session required")
	}
	m.execution.Cancel(child.ID)
	member := store.SessionTreeMember{ID: child.ID, ProjectID: child.ProjectID}
	if err := m.stops.StopRuntime(ctx, member, ""); err != nil {
		return err
	}
	m.graceful.Finish(child.ID)
	return m.sessions.SetSessionStatus(ctx, child.ID, api.SessionStatusIdle)
}

// CancellationInput is the parent cancellation envelope.
type CancellationInput struct {
	JobID            string
	AgentType        string
	ChildSessionID   string
	Reason           string
	Report           api.WorkerChangeReport
	CompletionReport workercompletion.WorkerCompletionReport
	Result           api.WorkerResult
}

// Append records a canceled worker leg on the coordinator transcript.
func (m *Cancellations) Append(ctx context.Context, parentID string, in CancellationInput) error {
	if m == nil {
		return fmt.Errorf("worker cancellation service unavailable")
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
	envelope := workercompletion.FormatWorkerCompletionEnvelope(workercompletion.WorkerCompletionEnvelope{
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
	if err := m.cards.Project(ctx, parentID, in.JobID, ws); err != nil {
		return err
	}
	if m.grounding != nil {
		m.grounding.Reset(parentID)
	}
	return nil
}

func (m *Cancellations) SetGrounding(grounding GroundingReset) { m.grounding = grounding }
