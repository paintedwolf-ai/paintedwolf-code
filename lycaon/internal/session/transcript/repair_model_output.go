package transcript

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/workercontext"
	"github.com/lycaon/lycaon/pkg/api"
)

// RepairPendingOutputs replays only unacknowledged immutable outputs.
// Its work is bounded by interrupted/degraded projections, not project history.
func (m *Service) RepairPendingOutputs(ctx context.Context) error {
	for {
		pending, err := m.store.ListPendingModelOutputProjections(ctx)
		if err != nil {
			return fmt.Errorf("list pending model output projections: %w", err)
		}
		for _, item := range pending {
			if err := m.repairModelOutputProjection(ctx, item); err != nil {
				return err
			}
		}
		if len(pending) < 256 {
			return nil
		}
	}
}

func (m *Service) repairModelOutputProjection(ctx context.Context, item store.PendingModelOutputProjection) error {
	output := item.Output
	ctx = workercontext.WithJob(ctx, item.WorkerJobID)
	message, messageErr := m.store.GetMessage(ctx, output.SessionID, output.MessageID)
	if messageErr != nil {
		if !errors.Is(messageErr, store.ErrMessageNotFound) {
			return fmt.Errorf("read model output projection %s: %w", output.ID, messageErr)
		}
		message = api.Message{
			ID: output.MessageID, Role: api.MessageRoleAssistant,
			Origin: api.MessageOriginModel, Authority: api.ContentAuthorityNone,
			TrustTier:  api.ContentTrustTierTrusted,
			Visibility: api.MessageVisibilityInternal, WorkerID: item.WorkerJobID, CreatedAt: output.CreatedAt,
		}
		if err := m.Append(ctx, output.SessionID, message); err != nil {
			return fmt.Errorf("restore model output placeholder %s: %w", output.ID, err)
		}
	}
	message.Content = output.Content
	if strings.TrimSpace(output.ToolCallsJSON) != "" {
		if err := json.Unmarshal([]byte(output.ToolCallsJSON), &message.ToolCalls); err != nil {
			return fmt.Errorf("decode model output tool calls %s: %w", output.ID, err)
		}
	}
	if strings.TrimSpace(output.ReasoningJSON) != "" && output.ReasoningJSON != "null" {
		if err := json.Unmarshal([]byte(output.ReasoningJSON), &message.ModelReasoning); err != nil {
			return fmt.Errorf("decode model output reasoning %s: %w", output.ID, err)
		}
	}
	// The immutable output is replayed through the screened update door, so a
	// recovered projection is screened exactly like the row it replaces.
	if err := m.Update(ctx, output.SessionID, output.MessageID, message); err != nil {
		return fmt.Errorf("repair model output projection %s: %w", output.ID, err)
	}
	if err := m.store.MarkModelOutputProjected(ctx, output.ID); err != nil {
		return fmt.Errorf("acknowledge model output projection %s: %w", output.ID, err)
	}
	return nil
}
