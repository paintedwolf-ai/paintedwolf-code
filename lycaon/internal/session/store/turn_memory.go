package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// BeginTurn creates a turn or adds an attempt to a recoverable turn.
func (t *memoryTurns) BeginTurn(ctx context.Context, in TurnStart) (TurnExecution, error) {
	return beginTurnMemory(ctx, t.core, in)
}

func beginTurnMemory(ctx context.Context, s *memoryCore, in TurnStart) (TurnExecution, error) {
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
				return resumeTurnLocked(s, turnIDs[i], in, execution.Attempt, now)
			}
		}
	}
	if len(in.SubmissionIDs) > 0 {
		if recoveredID := s.turnSubmissions[strings.TrimSpace(in.SubmissionIDs[0])]; recoveredID != "" &&
			s.turns[recoveredID].Status == TurnStatusRecovering {
			return resumeTurnLocked(s, recoveredID, in, execution.Attempt, now)
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
func resumeTurnLocked(s *memoryCore, turnID string, in TurnStart, attempt TurnAttempt, now time.Time) (TurnExecution, error) {
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

func (t *memoryTurns) CheckpointTurn(ctx context.Context, turnID, attemptID string, phase TurnPhase, checkpointJSON string) error {
	s := t.core
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
	return checkpointTurnLocked(s, turnID, attemptID, phase, checkpointJSON)
}

func checkpointTurnLocked(s *memoryCore, turnID, attemptID string, phase TurnPhase, checkpointJSON string) error {
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

func (t *memoryTurns) FinishTurn(ctx context.Context, turnID, attemptID string, status TurnStatus, finalOutputID, resultJSON, failure string) (Turn, error) {
	return finishTurnMemory(ctx, t.core, turnID, attemptID, status, finalOutputID, resultJSON, failure)
}

func finishTurnMemory(ctx context.Context, s *memoryCore, turnID, attemptID string, status TurnStatus, finalOutputID, resultJSON, failure string) (Turn, error) {
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

func (t *memoryTurns) RecoverTurns(ctx context.Context) ([]Turn, error) {
	return recoverTurnsMatching(ctx, t.core, "")
}

// RecoverTurnsForSession fences one session while other sessions remain active.
func (t *memoryTurns) RecoverTurnsForSession(ctx context.Context, sessionID string) ([]Turn, error) {
	if strings.TrimSpace(sessionID) == "" {
		return nil, fmt.Errorf("session id required")
	}
	return recoverTurnsMatching(ctx, t.core, sessionID)
}

func recoverTurnsMatching(ctx context.Context, s *memoryCore, sessionID string) ([]Turn, error) {
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
