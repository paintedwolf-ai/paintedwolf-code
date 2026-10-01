package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/promptresult"
	"strings"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/session/lifecycle"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/pkg/api"
)

type turnFinalizingCheckpoint struct {
	FinalOutputID string               `json:"final_output_id,omitempty"`
	Result        *promptresult.Result `json:"result,omitempty"`
}

func (m *Manager) beginDurableTurn(ctx context.Context, sess *api.Session, in PromptInput) (store.TurnExecution, error) {
	if m == nil || m.store == nil || sess == nil {
		return store.TurnExecution{}, fmt.Errorf("turn store unavailable")
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return store.TurnExecution{}, fmt.Errorf("encode durable turn input: %w", err)
	}
	submissionIDs := append([]string(nil), in.SubmissionIDs...)
	if id := strings.TrimSpace(in.SubmissionID); id != "" && len(submissionIDs) == 0 {
		submissionIDs = append(submissionIDs, id)
	}
	return m.store.BeginTurn(ctx, store.TurnStart{
		ID:            uuid.NewString(),
		SessionID:     sess.ID,
		ProjectID:     sess.ProjectID,
		Origin:        durableTurnOrigin(sess, in),
		InputJSON:     string(raw),
		SubmissionIDs: submissionIDs,
		WorkerJobID:   strings.TrimSpace(in.WorkerJobID),
	})
}

func durableTurnOrigin(sess *api.Session, in PromptInput) store.TurnOrigin {
	if sess != nil && sess.IsWorkerChild() {
		if in.HostSignal != nil {
			return store.TurnOriginWorkerCloseout
		}
		return store.TurnOriginWorker
	}
	if in.HostSignal == nil {
		return store.TurnOriginUser
	}
	switch in.HostSignal.Kind {
	case api.MessageKindHostLoopWake:
		return store.TurnOriginLoopWake
	default:
		return store.TurnOriginGroundingRetry
	}
}

func encodeTurnFinalizingCheckpoint(sessionID, messageID, finalOutputID string) (string, error) {
	var result *promptresult.Result
	if messageID = strings.TrimSpace(messageID); messageID != "" {
		result = &promptresult.Result{
			MessageID: messageID,
		}
	}
	raw, err := json.Marshal(turnFinalizingCheckpoint{
		FinalOutputID: strings.TrimSpace(finalOutputID), Result: result,
	})
	if err != nil {
		return "", fmt.Errorf("encode turn finalizing checkpoint: %w", err)
	}
	return string(raw), nil
}

func (m *Manager) finishDurableTurn(
	ctx context.Context,
	execution store.TurnExecution,
	finalOutputID string,
	resp *promptresult.Result,
	runErr error,
) error {
	status := store.TurnStatusComplete
	failure := ""
	if runErr != nil {
		status = store.TurnStatusFailed
		failure = runErr.Error()
		if errors.Is(runErr, context.Canceled) || errors.Is(runErr, lifecycle.ErrStopping) {
			status = store.TurnStatusInterrupted
		}
	}
	resultJSON := ""
	if resp != nil {
		raw, err := json.Marshal(resp)
		if err != nil {
			return fmt.Errorf("encode durable turn result: %w", err)
		}
		resultJSON = string(raw)
	}
	_, err := m.store.FinishTurn(
		ctx, execution.Turn.ID, execution.Attempt.ID, status,
		strings.TrimSpace(finalOutputID), resultJSON, failure,
	)
	if err != nil {
		return fmt.Errorf("finish durable turn: %w", err)
	}
	return nil
}
