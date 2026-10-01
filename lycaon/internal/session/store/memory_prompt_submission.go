package store

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ListOperationPromptAttachmentRetentions returns one operation's claims.
func (s *Memory) ListOperationPromptAttachmentRetentions(_ context.Context, operationID string) ([]PromptAttachmentRetention, error) {
	return s.listPromptAttachmentRetentions("", operationID, ""), nil
}

// PromptAttachmentBlobRetained reports whether durable history claims a blob.
func (s *Memory) PromptAttachmentBlobRetained(_ context.Context, projectID, blobID string) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.promptAttachmentBlobRetainedLocked(projectID, blobID), nil
}

// RecordPromptAttachmentBlob records one materialized body.
func (s *Memory) RecordPromptAttachmentBlob(_ context.Context, projectID, blobID string, byteSize int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	projectID = strings.TrimSpace(projectID)
	blobID = strings.TrimSpace(blobID)
	if projectID == "" || blobID == "" || byteSize < 0 {
		return fmt.Errorf("invalid prompt attachment blob")
	}
	byID := s.promptAttachmentBlobs[projectID]
	if byID == nil {
		byID = make(map[string]memoryPromptAttachmentBlob)
		s.promptAttachmentBlobs[projectID] = byID
	}
	byID[blobID] = memoryPromptAttachmentBlob{byteSize: byteSize, createdAt: time.Now().UTC()}
	return nil
}

// DeletePromptAttachmentBlob removes one materialized-body record.
func (s *Memory) DeletePromptAttachmentBlob(_ context.Context, projectID, blobID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	projectID = strings.TrimSpace(projectID)
	blobID = strings.TrimSpace(blobID)
	if s.promptAttachmentBlobRetainedLocked(projectID, blobID) {
		return ErrPromptAttachmentRetained
	}
	delete(s.promptAttachmentBlobs[projectID], blobID)
	return nil
}

func (s *Memory) ListPromptAttachmentReclaimCandidates(
	_ context.Context, projectID string, createdBefore time.Time, limit int,
) ([]string, error) {
	if limit <= 0 {
		return nil, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	type candidate struct {
		id        string
		createdAt time.Time
	}
	rows := make([]candidate, 0)
	projectID = strings.TrimSpace(projectID)
	for blobID, blob := range s.promptAttachmentBlobs[projectID] {
		if blob.createdAt.Before(createdBefore) && !s.promptAttachmentBlobRetainedLocked(projectID, blobID) {
			rows = append(rows, candidate{id: blobID, createdAt: blob.createdAt})
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if !rows[i].createdAt.Equal(rows[j].createdAt) {
			return rows[i].createdAt.Before(rows[j].createdAt)
		}
		return rows[i].id < rows[j].id
	})
	if len(rows) > limit {
		rows = rows[:limit]
	}
	out := make([]string, len(rows))
	for i := range rows {
		out[i] = rows[i].id
	}
	return out, nil
}

// PromptAttachmentStorageUsage reports recorded project bytes.
func (s *Memory) PromptAttachmentStorageUsage(_ context.Context, projectID string) (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var total int64
	for _, blob := range s.promptAttachmentBlobs[strings.TrimSpace(projectID)] {
		total += blob.byteSize
	}
	return total, nil
}

func (s *Memory) listPromptAttachmentRetentions(projectFilter, operationFilter, blobFilter string) []PromptAttachmentRetention {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.listPromptAttachmentRetentionsLocked(projectFilter, operationFilter, blobFilter)
}

func (s *Memory) promptAttachmentBlobRetainedLocked(projectID, blobID string) bool {
	return len(s.listPromptAttachmentRetentionsLocked(
		strings.TrimSpace(projectID), "", strings.TrimSpace(blobID),
	)) != 0
}

func (s *Memory) listPromptAttachmentRetentionsLocked(projectFilter, operationFilter, blobFilter string) []PromptAttachmentRetention {
	claims := make(map[string]PromptAttachmentRetention)
	add := func(projectID, operationID, blobID string) {
		projectID = strings.TrimSpace(projectID)
		operationID = strings.TrimSpace(operationID)
		blobID = strings.TrimSpace(blobID)
		if projectID == "" || operationID == "" || blobID == "" {
			return
		}
		claim := PromptAttachmentRetention{ProjectID: projectID, OperationID: operationID, BlobID: blobID}
		claims[projectID+"\x00"+operationID+"\x00"+blobID] = claim
	}
	for _, row := range s.promptSubmissions {
		if row.Status.Terminal() || s.sessions[row.SessionID] == nil {
			continue
		}
		for _, blobID := range normalizedBlobIDs(row.AttachmentBlobIDs) {
			add(row.ProjectID, row.ID, blobID)
		}
	}
	for sessionID, messages := range s.messages {
		sess := s.sessions[sessionID]
		if sess == nil {
			continue
		}
		for _, msg := range messages {
			for _, part := range msg.ContentParts {
				add(sess.ProjectID, msg.ID, part.BlobID)
			}
		}
	}
	out := make([]PromptAttachmentRetention, 0, len(claims))
	for _, claim := range claims {
		if projectFilter != "" && claim.ProjectID != projectFilter {
			continue
		}
		if operationFilter != "" && claim.OperationID != operationFilter {
			continue
		}
		if blobFilter != "" && claim.BlobID != blobFilter {
			continue
		}
		out = append(out, claim)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ProjectID != out[j].ProjectID {
			return out[i].ProjectID < out[j].ProjectID
		}
		if out[i].OperationID != out[j].OperationID {
			return out[i].OperationID < out[j].OperationID
		}
		return out[i].BlobID < out[j].BlobID
	})
	return out
}

func (s *Memory) PutPromptSubmission(_ context.Context, in PromptSubmission) (*PromptSubmission, bool, error) {
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

func (s *Memory) GetPromptSubmission(_ context.Context, id string) (*PromptSubmission, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	row, ok := s.promptSubmissions[id]
	if !ok {
		return nil, ErrPromptSubmissionNotFound
	}
	return &row, nil
}

func (s *Memory) ListQueuedUserPromptSubmissions(_ context.Context, sessionID string) ([]PromptSubmission, error) {
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
func (s *Memory) ListUnsettledUserPromptSubmissionIDs(_ context.Context, sessionID string) ([]string, error) {
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

func (s *Memory) ClaimPromptSubmission(_ context.Context, id string) (*PromptSubmission, bool, error) {
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

func (s *Memory) ClaimPromptSubmissions(_ context.Context, ids []string) ([]PromptSubmission, bool, error) {
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

func (s *Memory) FinishPromptSubmission(_ context.Context, id, claimToken string, status PromptSubmissionStatus, resultJSON string, failure PromptSubmissionFailure) error {
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

func (s *Memory) UpdateQueuedPromptSubmissionInputs(
	_ context.Context,
	updates []PromptSubmissionInputUpdate,
) (bool, error) {
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

func (s *Memory) CancelQueuedPromptSubmissions(_ context.Context, ids []string) error {
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

func (s *Memory) InterruptPromptSubmissionsBySession(_ context.Context, sessionID string) error {
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
func (s *Memory) InterruptRunningPromptSubmissionsBySession(_ context.Context, sessionID string) error {
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
func (s *Memory) RecoverPromptSubmissions(_ context.Context) ([]string, error) {
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
