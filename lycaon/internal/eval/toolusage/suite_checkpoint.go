package toolusage

import (
	"context"
	"net/http"

	wire "github.com/lycaon/lycaon/pkg/api"
)

type humanInputRequired struct{}

func (humanInputRequired) Error() string {
	return "human input requires a normal operator response; use --wait-for-input for a supervised run"
}

type FeedbackRequest struct {
	RunID string `json:"run_id"`
	wire.PendingFeedback
}

func (c *liveClient) pendingFeedback(ctx context.Context, sessionID string) (*FeedbackRequest, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/v1/sessions/"+sessionID+"/workflow-runs/active", nil)
	if err != nil {
		return nil, err
	}
	active, err := decodeJSON[wire.ActiveWorkflowRunResponse](c.do(req)) //nolint:bodyclose // decodeJSON closes the body.
	if err != nil {
		return nil, err
	}
	run := active.Run
	if run == nil || run.UI == nil || run.UI.PendingFeedback == nil {
		return nil, nil
	}
	return &FeedbackRequest{RunID: run.ID, PendingFeedback: *run.UI.PendingFeedback}, nil
}

func (c *liveClient) pendingCheckpoints(ctx context.Context, sessionID string) ([]wire.CheckpointEvent, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/v1/sessions/"+sessionID+"/checkpoints?status=pending&include_children=true", nil)
	if err != nil {
		return nil, err
	}
	res, err := decodeJSON[wire.CheckpointListResponse](c.do(req)) //nolint:bodyclose // decodeJSON closes the body.
	if err != nil {
		return nil, err
	}
	return res.Checkpoints, nil
}

func observeCaseHumanInput(result *CaseReport, wait bool, progress func(CaseReport) error) func([]wire.CheckpointEvent, *FeedbackRequest) error {
	seen := map[string]bool{}
	type questionID struct {
		run, phase string
		revision   int64
	}
	seenFeedback := map[questionID]bool{}
	return func(checkpoints []wire.CheckpointEvent, feedback *FeedbackRequest) error {
		changed := false
		pending := false
		for _, checkpoint := range checkpoints {
			if checkpoint.Status != wire.CheckpointStatusPending {
				continue
			}
			pending = true
			if seen[checkpoint.ID] {
				continue
			}
			seen[checkpoint.ID] = true
			result.CheckpointRequests = append(result.CheckpointRequests, checkpoint)
			changed = true
		}
		status := "running"
		if pending {
			status = "awaiting_approval"
		}
		if feedback != nil {
			pending = true
			status = "awaiting_input"
			key := questionID{feedback.RunID, feedback.PhaseID, feedback.IssuedRevision}
			if !seenFeedback[key] {
				seenFeedback[key] = true
				result.FeedbackRequests = append(result.FeedbackRequests, *feedback)
				changed = true
			}
		}
		if changed || result.Status != status {
			result.Status = status
			if err := progress(*result); err != nil {
				return err
			}
		}
		if pending && !wait {
			return humanInputRequired{}
		}
		return nil
	}
}
