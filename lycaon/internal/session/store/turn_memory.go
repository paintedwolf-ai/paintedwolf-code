package store

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// BeginTurn creates a turn or adds an attempt to a recoverable turn.
func (s *Memory) BeginTurn(ctx context.Context, in TurnStart) (TurnExecution, error) {
	if err := ctx.Err(); err != nil {
		return TurnExecution{}, err
	}
	in.ID = strings.TrimSpace(in.ID)
	if in.ID == "" {
		in.ID = uuid.NewString()
	}
	in.SessionID = strings.TrimSpace(in.SessionID)
	in.ProjectID = strings.TrimSpace(in.ProjectID)
	if in.SessionID == "" || in.ProjectID == "" || !validTurnOrigin(in.Origin) {
		return TurnExecution{}, fmt.Errorf("turn session, project, and origin are required")
	}
	now := time.Now().UTC()
	attemptID := uuid.NewString()
	execution := TurnExecution{
		Turn: Turn{
			ID: in.ID, SessionID: in.SessionID, ProjectID: in.ProjectID,
			Origin: in.Origin, InputJSON: in.InputJSON, Status: TurnStatusRunning,
			Revision: 1, ActiveAttemptID: attemptID, CreatedAt: now, UpdatedAt: now, ProgressedAt: now,
		},
		Attempt: TurnAttempt{
			ID: attemptID, TurnID: in.ID, Attempt: 1, Status: TurnStatusRunning,
			Phase: TurnPhasePreparing, CheckpointJSON: "{}", StartedAt: now, UpdatedAt: now,
		},
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sessions[in.SessionID]; !ok {
		return TurnExecution{}, ErrSessionNotFound
	}
	if workerJobID := strings.TrimSpace(in.WorkerJobID); workerJobID != "" {
		turnIDs := s.workerTurns[workerJobID]
		for i := len(turnIDs) - 1; i >= 0; i-- {
			turn := s.turns[turnIDs[i]]
			if turn.Origin == in.Origin &&
				(turn.Status == TurnStatusRecovering || turn.Status == TurnStatusFailed || turn.Status == TurnStatusInterrupted) {
				return s.resumeTurnLocked(turnIDs[i], in, execution.Attempt, now)
			}
		}
	}
	if len(in.SubmissionIDs) > 0 {
		if recoveredID := s.turnSubmissions[strings.TrimSpace(in.SubmissionIDs[0])]; recoveredID != "" &&
			s.turns[recoveredID].Status == TurnStatusRecovering {
			return s.resumeTurnLocked(recoveredID, in, execution.Attempt, now)
		}
	}
	if _, exists := s.turns[in.ID]; exists {
		return TurnExecution{}, fmt.Errorf("turn already exists: %s", in.ID)
	}
	s.turns[in.ID] = execution.Turn
	s.turnHeads[in.SessionID] = in.ID
	s.turnAttempts[in.ID] = []TurnAttempt{execution.Attempt}
	for _, submissionID := range in.SubmissionIDs {
		if submissionID = strings.TrimSpace(submissionID); submissionID != "" {
			s.turnSubmissions[submissionID] = in.ID
		}
	}
	if workerJobID := strings.TrimSpace(in.WorkerJobID); workerJobID != "" {
		s.workerTurns[workerJobID] = append(s.workerTurns[workerJobID], in.ID)
	}
	return execution, nil
}

// resumeTurnLocked adds a fresh attempt to a turn being retried.
func (s *Memory) resumeTurnLocked(turnID string, in TurnStart, attempt TurnAttempt, now time.Time) (TurnExecution, error) {
	turn := s.turns[turnID]
	if turn.SessionID != in.SessionID || turn.ProjectID != in.ProjectID {
		return TurnExecution{}, fmt.Errorf("recovering turn identity mismatch: %s", turnID)
	}
	attempt.TurnID = turnID
	attempt.Attempt = len(s.turnAttempts[turnID]) + 1
	turn.Status = TurnStatusRunning
	s.turnHeads[in.SessionID] = turnID
	turn.Revision++
	turn.ActiveAttemptID = attempt.ID
	turn.Error = ""
	turn.UpdatedAt = now
	turn.CompletedAt = nil
	s.turns[turnID] = turn
	s.turnAttempts[turnID] = append(s.turnAttempts[turnID], attempt)
	return TurnExecution{Turn: turn, Attempt: attempt}, nil
}

func (s *Memory) CheckpointTurn(ctx context.Context, turnID, attemptID string, phase TurnPhase, checkpointJSON string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validTurnPhase(phase) || phase == TurnPhaseComplete {
		return fmt.Errorf("invalid active turn phase %q", phase)
	}
	if strings.TrimSpace(checkpointJSON) == "" {
		checkpointJSON = "{}"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.checkpointTurnLocked(turnID, attemptID, phase, checkpointJSON)
}

func (s *Memory) checkpointTurnLocked(turnID, attemptID string, phase TurnPhase, checkpointJSON string) error {
	attempts := s.turnAttempts[turnID]
	for i := range attempts {
		if attempts[i].ID != attemptID || attempts[i].Status != TurnStatusRunning {
			continue
		}
		now := time.Now().UTC()
		attempts[i].Phase = phase
		attempts[i].CheckpointJSON = checkpointJSON
		attempts[i].UpdatedAt = now
		s.turnAttempts[turnID] = attempts
		turn := s.turns[turnID]
		turn.Revision++
		turn.UpdatedAt = now
		turn.ProgressedAt = now
		s.turns[turnID] = turn
		return nil
	}
	return fmt.Errorf("turn attempt claim lost: %s", attemptID)
}

func (s *Memory) ProjectLiveModelOutput(ctx context.Context, out LiveModelOutput) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if out.ID == "" || out.TurnAttemptID == "" || out.SessionID == "" || out.MessageID == "" || out.Iteration <= 0 {
		return fmt.Errorf("live model output identity required")
	}
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.liveModelOutputs[out.ID]; ok {
		out.CreatedAt = existing.CreatedAt
	} else if out.CreatedAt.IsZero() {
		out.CreatedAt = now
	}
	out.UpdatedAt = now
	s.liveModelOutputs[out.ID] = out
	return nil
}

func (s *Memory) SettleModelOutput(ctx context.Context, out ModelOutput) (ModelOutput, error) {
	if err := ctx.Err(); err != nil {
		return ModelOutput{}, err
	}
	if out.ID == "" || out.TurnAttemptID == "" || out.SessionID == "" || out.MessageID == "" || out.Iteration <= 0 {
		return ModelOutput{}, fmt.Errorf("model output identity required")
	}
	now := time.Now().UTC()
	if out.CreatedAt.IsZero() {
		out.CreatedAt = now
	}
	if out.SettledAt.IsZero() {
		out.SettledAt = now
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.modelOutputs[out.ID]; ok {
		if !sameModelOutput(existing, out) {
			return ModelOutput{}, fmt.Errorf("model output id %s reused with different content or identity", out.ID)
		}
		return existing, nil
	}
	active := false
	for _, turn := range s.turns {
		active = active || (turn.SessionID == out.SessionID && turn.ActiveAttemptID == out.TurnAttemptID && turn.Status == TurnStatusRunning)
	}
	if !active {
		return ModelOutput{}, fmt.Errorf("model output attempt claim lost")
	}
	for _, existing := range s.modelOutputs {
		if existing.TurnAttemptID == out.TurnAttemptID && existing.Iteration == out.Iteration {
			return ModelOutput{}, fmt.Errorf("model iteration already settled")
		}
	}
	s.modelOutputs[out.ID] = out
	delete(s.liveModelOutputs, out.ID)
	return out, nil
}

// sameModelOutput compares the in-memory output values directly.
func sameModelOutput(committed, proposed ModelOutput) bool {
	return committed.ID == proposed.ID &&
		committed.TurnAttemptID == proposed.TurnAttemptID &&
		committed.SessionID == proposed.SessionID &&
		committed.Iteration == proposed.Iteration &&
		committed.MessageID == proposed.MessageID &&
		committed.ProviderID == proposed.ProviderID &&
		committed.Model == proposed.Model &&
		committed.Content == proposed.Content &&
		committed.ToolCallsJSON == proposed.ToolCallsJSON &&
		committed.ReasoningJSON == proposed.ReasoningJSON &&
		committed.FinishReason == proposed.FinishReason && committed.Scripted == proposed.Scripted
}

func (s *Memory) ListPendingModelOutputProjections(ctx context.Context) ([]PendingModelOutputProjection, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []PendingModelOutputProjection
	for id, output := range s.modelOutputs {
		if _, ok := s.projectedModelOutputs[id]; !ok {
			workerJobID := ""
			for jobID, turnIDs := range s.workerTurns {
				for _, turnID := range turnIDs {
					for _, attempt := range s.turnAttempts[turnID] {
						if attempt.ID == output.TurnAttemptID {
							workerJobID = jobID
							break
						}
					}
					if workerJobID != "" {
						break
					}
				}
				if workerJobID != "" {
					break
				}
			}
			out = append(out, PendingModelOutputProjection{Output: output, WorkerJobID: workerJobID})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Output.SettledAt.Equal(out[j].Output.SettledAt) {
			return out[i].Output.ID < out[j].Output.ID
		}
		return out[i].Output.SettledAt.Before(out[j].Output.SettledAt)
	})
	if len(out) > 256 {
		out = out[:256]
	}
	return out, nil
}

func (s *Memory) MarkModelOutputProjected(ctx context.Context, outputID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(outputID) == "" {
		return fmt.Errorf("model output projection identity required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	output, ok := s.modelOutputs[outputID]
	if !ok {
		return fmt.Errorf("model output not found: %s", outputID)
	}
	for _, message := range s.messages[output.SessionID] {
		if message.ID == output.MessageID && message.Seq > 0 {
			s.projectedModelOutputs[outputID] = message.Seq
			return nil
		}
	}
	return fmt.Errorf("model output projection not found: %s", outputID)
}

func (s *Memory) FinishTurn(ctx context.Context, turnID, attemptID string, status TurnStatus, finalOutputID, resultJSON, failure string) (Turn, error) {
	if err := ctx.Err(); err != nil {
		return Turn{}, err
	}
	if status != TurnStatusComplete && status != TurnStatusFailed && status != TurnStatusInterrupted {
		return Turn{}, fmt.Errorf("invalid terminal turn status %q", status)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	turn, ok := s.turns[turnID]
	if !ok || turn.ActiveAttemptID != attemptID || turn.Status != TurnStatusRunning {
		return Turn{}, fmt.Errorf("turn head claim lost: %s", turnID)
	}
	attempts := s.turnAttempts[turnID]
	found := false
	now := time.Now().UTC()
	for i := range attempts {
		if attempts[i].ID == attemptID && attempts[i].Status == TurnStatusRunning {
			attempts[i].Status = status
			attempts[i].Error = failure
			attempts[i].UpdatedAt = now
			attempts[i].CompletedAt = &now
			if status == TurnStatusComplete {
				attempts[i].Phase = TurnPhaseComplete
			}
			found = true
			break
		}
	}
	if !found {
		return Turn{}, fmt.Errorf("turn attempt claim lost: %s", attemptID)
	}
	s.turnAttempts[turnID] = attempts
	turn.Status = status
	turn.FinalOutputID = finalOutputID
	turn.ResultJSON = resultJSON
	turn.Error = failure
	turn.Revision++
	turn.UpdatedAt = now
	turn.CompletedAt = &now
	turn.ProgressedAt = now
	s.turns[turnID] = turn
	return turn, nil
}

func (s *Memory) RecoverTurns(ctx context.Context) ([]Turn, error) {
	return s.recoverTurnsMatching(ctx, "")
}

// RecoverTurnsForSession fences one session while other sessions remain active.
func (s *Memory) RecoverTurnsForSession(ctx context.Context, sessionID string) ([]Turn, error) {
	if strings.TrimSpace(sessionID) == "" {
		return nil, fmt.Errorf("session id required")
	}
	return s.recoverTurnsMatching(ctx, sessionID)
}

func (s *Memory) recoverTurnsMatching(ctx context.Context, sessionID string) ([]Turn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	var recovered []Turn
	for id, turn := range s.turns {
		if turn.Status != TurnStatusRunning {
			continue
		}
		if sessionID != "" && turn.SessionID != sessionID {
			continue
		}
		attempts := s.turnAttempts[id]
		finalized := false
		for i := range attempts {
			if attempts[i].ID != turn.ActiveAttemptID || attempts[i].Status != TurnStatusRunning || attempts[i].Phase != TurnPhaseFinalizing {
				continue
			}
			var checkpoint struct {
				FinalOutputID string          `json:"final_output_id"`
				Result        json.RawMessage `json:"result"`
			}
			if err := json.Unmarshal([]byte(attempts[i].CheckpointJSON), &checkpoint); err != nil {
				break
			}
			attempts[i].Status = TurnStatusComplete
			attempts[i].Phase = TurnPhaseComplete
			attempts[i].UpdatedAt = now
			attempts[i].CompletedAt = &now
			turn.Status = TurnStatusComplete
			turn.FinalOutputID = checkpoint.FinalOutputID
			if len(checkpoint.Result) > 0 && string(checkpoint.Result) != "null" {
				turn.ResultJSON = string(checkpoint.Result)
			}
			turn.Revision++
			turn.UpdatedAt = now
			turn.CompletedAt = &now
			s.turnAttempts[id] = attempts
			s.turns[id] = turn
			finalized = true
			break
		}
		if finalized {
			for submissionID, linkedTurnID := range s.turnSubmissions {
				if linkedTurnID != id {
					continue
				}
				submission := s.promptSubmissions[submissionID]
				if submission.Status != PromptSubmissionRunning {
					continue
				}
				submission.Status = PromptSubmissionComplete
				submission.ResultJSON = turn.ResultJSON
				submission.Error = ""
				submission.CompletedAt = &now
				s.promptSubmissions[submissionID] = submission
			}
			continue
		}
		for i := range attempts {
			if attempts[i].Status == TurnStatusRunning {
				attempts[i].Status = TurnStatusInterrupted
				attempts[i].Error = "host stopped during turn execution"
				attempts[i].UpdatedAt = now
				attempts[i].CompletedAt = &now
			}
		}
		s.turnAttempts[id] = attempts
		if turn.Origin == TurnOriginUser || turn.Origin == TurnOriginWorker {
			turn.Status = TurnStatusRecovering
		} else {
			turn.Status = TurnStatusInterrupted
			turn.Error = "host stopped during turn execution"
			turn.CompletedAt = &now
		}
		turn.Revision++
		turn.UpdatedAt = now
		s.turns[id] = turn
		if turn.Status == TurnStatusRecovering && len(recovered) < 256 {
			recovered = append(recovered, turn)
		}
	}
	if sessionID == "" {
		s.liveModelOutputs = make(map[string]LiveModelOutput)
	} else {
		for id, out := range s.liveModelOutputs {
			if out.SessionID == sessionID {
				delete(s.liveModelOutputs, id)
			}
		}
	}
	return recovered, nil
}
