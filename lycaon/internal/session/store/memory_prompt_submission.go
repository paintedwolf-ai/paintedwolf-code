package store

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
)

func (sub *memorySubmissions) PutPromptSubmission(_ context.Context, in PromptSubmission) (*PromptSubmission, bool, error) {
	s := sub.core
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := in.validateAuthorship(); err != nil {
		return nil, false, err
	}
	if current, ok := s.promptSubmissions[in.ID]; ok {
		if !current.replays(in) {
			return nil, false, &PromptSubmissionConflictError{ID: in.ID}
		}
		copy := current
		return &copy, false, nil
	}
	if in.CreatedAt.IsZero() {
		in.CreatedAt = time.Now().UTC()
	}
	s.promptSubmissionSeq[in.SessionID]++
	in.AdmissionSeq = s.promptSubmissionSeq[in.SessionID]
	in.Status = PromptSubmissionQueued
	s.promptSubmissions[in.ID] = in
	copy := in
	return &copy, true, nil
}

func (sub *memorySubmissions) GetPromptSubmission(_ context.Context, id string) (*PromptSubmission, error) {
	s := sub.core
	s.mu.RLock()
	defer s.mu.RUnlock()
	row, ok := s.promptSubmissions[id]
	if !ok {
		return nil, ErrPromptSubmissionNotFound
	}
	return &row, nil
}

func (sub *memorySubmissions) ListQueuedUserPromptSubmissions(_ context.Context, sessionID string) ([]PromptSubmission, error) {
	s := sub.core
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]PromptSubmission, 0)
	for _, row := range s.promptSubmissions {
		if row.SessionID == sessionID && row.Status == PromptSubmissionQueued && row.Origin == PromptSubmissionOriginUser {
			out = append(out, row)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].AdmissionSeq < out[j].AdmissionSeq
	})
	return out, nil
}

// ListUnsettledUserPromptSubmissionIDs lists human prompts that are queued or
// running, in admission order.
func (sub *memorySubmissions) ListUnsettledUserPromptSubmissionIDs(_ context.Context, sessionID string) ([]string, error) {
	s := sub.core
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows := make([]PromptSubmission, 0)
	for _, row := range s.promptSubmissions {
		if row.SessionID == sessionID && row.Origin == PromptSubmissionOriginUser &&
			(row.Status == PromptSubmissionQueued || row.Status == PromptSubmissionRunning) {
			rows = append(rows, row)
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].AdmissionSeq < rows[j].AdmissionSeq
	})
	ids := make([]string, len(rows))
	for i := range rows {
		ids[i] = rows[i].ID
	}
	return ids, nil
}

func (sub *memorySubmissions) ClaimPromptSubmission(_ context.Context, id string) (*PromptSubmission, bool, error) {
	s := sub.core
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.promptSubmissions[id]
	if !ok {
		return nil, false, ErrPromptSubmissionNotFound
	}
	if row.Status != PromptSubmissionQueued {
		return &row, false, nil
	}
	now := time.Now().UTC()
	row.Status = PromptSubmissionRunning
	row.ClaimToken = uuid.NewString()
	row.StartedAt = &now
	s.promptSubmissions[id] = row
	return &row, true, nil
}

func (sub *memorySubmissions) ClaimPromptSubmissions(_ context.Context, ids []string) ([]PromptSubmission, bool, error) {
	return claimPromptSubmissionsMemory(sub.core, ids)
}

func claimPromptSubmissionsMemory(s *memoryCore, ids []string) ([]PromptSubmission, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if id == "" {
			return nil, false, fmt.Errorf("claim prompt submissions: empty id")
		}
		if _, exists := seen[id]; exists {
			return nil, false, fmt.Errorf("claim prompt submissions: duplicate id %s", id)
		}
		seen[id] = struct{}{}
		row, ok := s.promptSubmissions[id]
		if !ok || row.Status != PromptSubmissionQueued {
			return nil, false, nil
		}
	}
	now := time.Now().UTC()
	out := make([]PromptSubmission, 0, len(ids))
	for _, id := range ids {
		row := s.promptSubmissions[id]
		row.Status = PromptSubmissionRunning
		row.ClaimToken = uuid.NewString()
		started := now
		row.StartedAt = &started
		s.promptSubmissions[id] = row
		out = append(out, row)
	}
	return out, true, nil
}

func (sub *memorySubmissions) FinishPromptSubmission(_ context.Context, id, claimToken string, status PromptSubmissionStatus, resultJSON string, failure PromptSubmissionFailure) error {
	s := sub.core
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.promptSubmissions[id]
	if !ok || row.Status != PromptSubmissionRunning || row.ClaimToken != claimToken {
		return fmt.Errorf("prompt submission %s claim lost", id)
	}
	now := time.Now().UTC()
	row.Status = status
	row.ResultJSON = resultJSON
	row.Error = failure.Message
	row.ErrorCode = failure.Code
	row.CompletedAt = &now
	s.promptSubmissions[id] = row
	return nil
}

func (sub *memorySubmissions) UpdateQueuedPromptSubmissionInputs(
	_ context.Context,
	updates []PromptSubmissionInputUpdate,
) (bool, error) {
	s := sub.core
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, update := range updates {
		row, ok := s.promptSubmissions[update.ID]
		if !ok || row.Status != PromptSubmissionQueued {
			return false, nil
		}
	}
	for _, update := range updates {
		row := s.promptSubmissions[update.ID]
		row.InputJSON = update.InputJSON
		s.promptSubmissions[update.ID] = row
	}
	return true, nil
}

func (sub *memorySubmissions) CancelQueuedPromptSubmissions(_ context.Context, ids []string) error {
	s := sub.core
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	for _, id := range ids {
		row, ok := s.promptSubmissions[id]
		if !ok || row.Status != PromptSubmissionQueued {
			continue
		}
		row.Status = PromptSubmissionCanceled
		row.Error = "removed from the next-turn queue before it ran"
		row.CompletedAt = &now
		s.promptSubmissions[id] = row
	}
	return nil
}

func (sub *memorySubmissions) InterruptPromptSubmissionsBySession(_ context.Context, sessionID string) error {
	s := sub.core
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	for id, row := range s.promptSubmissions {
		if row.SessionID != sessionID {
			continue
		}
		switch row.Status {
		case PromptSubmissionQueued:
			row.Status = PromptSubmissionCanceled
			row.Error = "session stopped before the queued prompt ran"
		case PromptSubmissionRunning:
			row.Status = PromptSubmissionInterrupted
			row.Error = "session stopped while provider work was in flight"
		default:
			continue
		}
		row.CompletedAt = &now
		s.promptSubmissions[id] = row
	}
	return nil
}

// InterruptRunningPromptSubmissionsBySession interrupts running prompt submissions.
func (sub *memorySubmissions) InterruptRunningPromptSubmissionsBySession(_ context.Context, sessionID string) error {
	s := sub.core
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	for id, row := range s.promptSubmissions {
		if row.SessionID != sessionID || row.Status != PromptSubmissionRunning {
			continue
		}
		row.Status = PromptSubmissionInterrupted
		row.Error = "session stopped while provider work was in flight"
		row.CompletedAt = &now
		s.promptSubmissions[id] = row
	}
	return nil
}

// RecoverPromptSubmissions returns runnable queued user turns.
func (sub *memorySubmissions) RecoverPromptSubmissions(_ context.Context) ([]string, error) {
	return recoverPromptSubmissionsMemory(sub.core)
}

func recoverPromptSubmissionsMemory(s *memoryCore) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var queued []PromptSubmission
	for id, row := range s.promptSubmissions {
		switch {
		case row.Status == PromptSubmissionQueued && row.Origin.HostInitiated():
			now := time.Now().UTC()
			row.Status = PromptSubmissionInterrupted
			row.Error = "host stopped before the host-initiated turn started"
			row.CompletedAt = &now
			s.promptSubmissions[id] = row
		case row.Status == PromptSubmissionQueued:
			queued = append(queued, row)
		case row.Status == PromptSubmissionRunning && row.Origin == PromptSubmissionOriginUser:
			row.Status = PromptSubmissionQueued
			row.ClaimToken = ""
			row.StartedAt = nil
			row.CompletedAt = nil
			row.Error = ""
			s.promptSubmissions[id] = row
			queued = append(queued, row)
		case row.Status == PromptSubmissionRunning:
			now := time.Now().UTC()
			row.Status = PromptSubmissionInterrupted
			row.Error = "host stopped during a host-initiated turn"
			row.CompletedAt = &now
			s.promptSubmissions[id] = row
		}
	}
	// Submission order matters: recovery rebuilds each session's next-turn draft.
	sort.Slice(queued, func(i, j int) bool {
		if queued[i].SessionID == queued[j].SessionID {
			return queued[i].AdmissionSeq < queued[j].AdmissionSeq
		}
		return queued[i].SessionID < queued[j].SessionID
	})
	ids := make([]string, 0, len(queued))
	for _, row := range queued {
		ids = append(ids, row.ID)
	}
	return ids, nil
}
