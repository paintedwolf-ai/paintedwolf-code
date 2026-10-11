package workerresults

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/pkg/api"
)

// HoldInput is the parent hold envelope.
type HoldInput struct {
	JobID          string
	AgentType      string
	ChildSessionID string
	Report         api.WorkerChangeReport
}

// Hold projects a held worker state into its task card.
func (m *Cards) Hold(ctx context.Context, parentID string, in HoldInput) error {
	if m == nil {
		return fmt.Errorf("worker card service unavailable")
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
		ObjectivesMet: []string{"Worker leg held"},
		RemainingRisk: []string{"Workflow paused — worker held pending resume"},
	}
	report.Normalize()
	agent := strings.TrimSpace(in.AgentType)
	if agent == "" {
		agent = "worker"
	}
	envelope := workercompletion.FormatWorkerCompletionEnvelope(workercompletion.WorkerCompletionEnvelope{
		JobID:          in.JobID,
		ChildSessionID: in.ChildSessionID,
		AgentType:      in.AgentType,
		State:          string(api.WorkerSummaryStatusHeld),
		Summary:        agent + " held",
		Proof:          proof,
		Report:         report,
	})
	ws := &api.WorkerSummaryMeta{
		WorkerID:       in.JobID,
		ChildSessionID: in.ChildSessionID,
		AgentType:      in.AgentType,
		Status:         api.WorkerSummaryStatusHeld,
		Envelope:       envelope,
	}
	return m.Project(ctx, parentID, in.JobID, ws)
}
