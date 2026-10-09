package promptsource

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/toolpresentation"
	"github.com/lycaon/lycaon/internal/session/transcript"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/pkg/api"
)

type ProjectionRepository interface {
	AdmitModelResponse(context.Context, string, string) error
	AppendDraftVersion(ctx context.Context, sessionID, slotID, body, outcomeCode string) (int, error)
	CheckpointTurn(ctx context.Context, turnID, attemptID string, phase store.TurnPhase, checkpointJSON string) error
	CountDraftVersions(ctx context.Context, sessionID, slotID string) (int, error)
	MarkModelOutputProjected(ctx context.Context, outputID string) error
	SettleModelOutput(ctx context.Context, out store.ModelOutput) (store.ModelOutput, error)
}

type WorkflowReviews interface {
	RecordReviewToolResult(ctx context.Context, sessionID string, msg api.Message) error
}

type Projection struct {
	Events     *events.Publisher
	Rejections *workeroutcomes.PeerRejectionFeed
	Sessions   ProjectionRepository
	Stash      *toolpresentation.Stash
	Transcript *transcript.Service
	Workflow   WorkflowAsks
	Reviews    WorkflowReviews
}

func (m *Projection) Build() promptloop.ProjectionDeps {
	appendMsgs := m.Transcript.Append
	if m != nil && m.Reviews != nil {
		appendMsgs = func(ctx context.Context, sessionID string, msgs ...api.Message) error {
			if err := m.Transcript.Append(ctx, sessionID, msgs...); err != nil {
				return err
			}
			for _, msg := range msgs {
				if err := m.Reviews.RecordReviewToolResult(ctx, sessionID, msg); err != nil {
					return err
				}
			}
			return nil
		}
	}
	deps := promptloop.ProjectionDeps{
		Events:                  m.Events,
		RedactMessageForStorage: m.Transcript.Redact,
		AppendMessages:          appendMsgs,
		Streams:                 m.Transcript.Streams,
	}
	deps.OnToolReject = func(ctx context.Context, sessionID, toolCallID, code, content string, facts guidance.ToolResultFacts) {
		if m != nil && m.Stash != nil {
			m.Stash.Put(sessionID, toolCallID, code, content)
		}
		if m != nil {
			m.Rejections.RecordForChild(ctx, sessionID, code, content, facts)
		}
	}
	deps.AnnouncePendingToolAsk = func(ctx context.Context, sessionID string) {
		if m != nil && m.Workflow != nil {
			m.Workflow.AnnouncePendingAsk(ctx, sessionID)
		}
	}
	deps.UpdateMessage = func(ctx context.Context, sessionID, messageID string, msg api.Message) error {
		if m == nil {
			return fmt.Errorf("session store not configured")
		}
		return m.Transcript.Update(ctx, sessionID, messageID, msg)
	}

	deps.AdmitModelResponse = m.Sessions.AdmitModelResponse
	deps.SettleModelOutput = func(ctx context.Context, out store.ModelOutput) (store.ModelOutput, error) {
		if m == nil || m.Sessions == nil {
			return store.ModelOutput{}, fmt.Errorf("session store not configured")
		}
		return m.Sessions.SettleModelOutput(ctx, out)
	}
	deps.MarkModelOutputProjected = func(ctx context.Context, outputID string) error {
		if m == nil || m.Sessions == nil {
			return fmt.Errorf("session store not configured")
		}
		return m.Sessions.MarkModelOutputProjected(ctx, outputID)
	}
	deps.CheckpointTurn = func(ctx context.Context, turnID, attemptID string, phase store.TurnPhase, checkpointJSON string) error {
		if m == nil || m.Sessions == nil {
			return fmt.Errorf("session store not configured")
		}
		return m.Sessions.CheckpointTurn(ctx, turnID, attemptID, phase, checkpointJSON)
	}
	deps.AppendDraftVersion = func(ctx context.Context, sessionID, slotID, body, outcomeCode string) (int, error) {
		if m == nil {
			return 0, fmt.Errorf("session store not configured")
		}
		return m.Sessions.AppendDraftVersion(ctx, sessionID, slotID, body, outcomeCode)
	}
	deps.CountDraftVersions = func(ctx context.Context, sessionID, slotID string) (int, error) {
		if m == nil {
			return 0, fmt.Errorf("session store not configured")
		}
		return m.Sessions.CountDraftVersions(ctx, sessionID, slotID)
	}

	return deps
}
