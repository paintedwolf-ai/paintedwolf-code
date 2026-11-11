package store

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/lycaon/lycaon/pkg/api"
)

func (r *memoryRewind) PrepareRewind(ctx context.Context, op RewindOperation) error {
	s := r.core
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if current, ok := s.rewindOperations[op.ID]; ok {
		if current.SessionID != op.SessionID || current.AnchorMessageID != op.AnchorMessageID || current.InputDigest != op.InputDigest {
			return fmt.Errorf("rewind operation already exists")
		}
		if current.Status != "rolled_back" {
			return fmt.Errorf("rewind operation already exists")
		}
	}
	op.Status = "prepared"
	op.Error = ""
	s.rewindOperations[op.ID] = op
	s.rewindCreatedAt[op.ID] = time.Now().UTC()
	return nil
}

func (r *memoryRewind) GetRewindOperation(ctx context.Context, id string) (*RewindOperation, error) {
	s := r.core
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	op, ok := s.rewindOperations[id]
	if !ok {
		return nil, nil
	}
	copy := op
	return &copy, nil
}

func (r *memoryRewind) SetRewindPhase(ctx context.Context, id, status, detail string) error {
	s := r.core
	if err := ctx.Err(); err != nil {
		return err
	}
	switch status {
	case "applying", "files_applied", "rolled_back", "diverged":
	default:
		return fmt.Errorf("invalid rewind phase %q", status)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	op, ok := s.rewindOperations[id]
	if !ok || op.Status == "committed" {
		return fmt.Errorf("rewind operation %s not mutable", id)
	}
	op.Status = status
	op.Error = strings.TrimSpace(detail)
	s.rewindOperations[id] = op
	return nil
}

func (r *memoryRewind) CommitRewind(ctx context.Context, id, sessionID, anchorMessageID string, response api.RewindSessionResponse) (int, error) {
	return commitRewindMemory(ctx, r.core, id, sessionID, anchorMessageID, response)
}

func commitRewindMemory(ctx context.Context, s *memoryCore, id, sessionID, anchorMessageID string, response api.RewindSessionResponse) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	op, ok := s.rewindOperations[id]
	if !ok || op.SessionID != sessionID || op.AnchorMessageID != anchorMessageID {
		return 0, fmt.Errorf("rewind operation %s not found", id)
	}
	if op.Status == "committed" {
		return 0, nil
	}
	if op.Status != "files_applied" {
		return 0, fmt.Errorf("rewind operation %s is %s", id, op.Status)
	}
	if _, ok := s.sessions[sessionID]; !ok {
		return 0, ErrSessionNotFound
	}
	msgs := s.messages[sessionID]
	for i := range msgs {
		if msgs[i].ID != anchorMessageID {
			continue
		}
		removed := len(msgs) - i
		for _, removedMessage := range msgs[i:] {
			delete(s.chunkProjections[sessionID], removedMessage.ID)
		}
		s.messages[sessionID] = append([]api.Message(nil), msgs[:i]...)
		now := time.Now().UTC()
		for submissionID, row := range s.promptSubmissions {
			if row.SessionID != sessionID || row.Status != PromptSubmissionQueued {
				continue
			}
			row.Status = PromptSubmissionCanceled
			row.Error = "next-turn queue discarded before it ran"
			row.CompletedAt = &now
			s.promptSubmissions[submissionID] = row
		}
		response.TruncatedMessageCount = removed
		raw, err := json.Marshal(response)
		if err != nil {
			return 0, err
		}
		op.Status = "committed"
		op.Error = ""
		op.ResponseJSON = string(raw)
		s.rewindOperations[id] = op
		return removed, nil
	}
	return 0, ErrMessageNotFound
}

func (r *memoryRewind) RewindOperationsForRecovery(ctx context.Context) ([]RewindOperation, error) {
	return rewindOperationsForRecoveryMatching(ctx, r.core, "")
}

// RewindOperationsForRecoverySession scopes RewindOperationsForRecovery to
// one session, for on-demand repair after a panic caught mid-rewind.
func (r *memoryRewind) RewindOperationsForRecoverySession(ctx context.Context, sessionID string) ([]RewindOperation, error) {
	if strings.TrimSpace(sessionID) == "" {
		return nil, fmt.Errorf("session id required")
	}
	return rewindOperationsForRecoveryMatching(ctx, r.core, sessionID)
}

func rewindOperationsForRecoveryMatching(ctx context.Context, s *memoryCore, sessionID string) ([]RewindOperation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]RewindOperation, 0, len(s.rewindOperations))
	for _, op := range s.rewindOperations {
		if sessionID != "" && op.SessionID != sessionID {
			continue
		}
		switch op.Status {
		case "prepared", "applying", "files_applied":
			out = append(out, op)
		}
	}
	return out, nil
}

// CommittedRewindOperationsForSweep lists committed rewinds awaiting the
// boot-only disk GC pass (see Manager.sweepCommittedRewinds).
func (r *memoryRewind) CommittedRewindOperationsForSweep(ctx context.Context) ([]RewindOperationSweepItem, error) {
	s := r.core
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]RewindOperationSweepItem, 0)
	for id, op := range s.rewindOperations {
		if op.Status != "committed" {
			continue
		}
		out = append(out, RewindOperationSweepItem{
			ID: op.ID, SessionID: op.SessionID, AnchorMessageID: op.AnchorMessageID,
			ProjectDir:          op.ProjectDir,
			CheckpointAnchorIDs: op.CheckpointAnchorIDs, CreatedAt: s.rewindCreatedAt[id],
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}

// DeleteRewindOperation removes one committed rewind row once its retention
// window (olderThan) has passed. A no-op (not an error) if the row already
// moved past 'committed', or hasn't aged out yet.
func (r *memoryRewind) DeleteRewindOperation(ctx context.Context, id string, olderThan time.Time) error {
	s := r.core
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	op, ok := s.rewindOperations[id]
	if !ok || op.Status != "committed" {
		return nil
	}
	if !s.rewindCreatedAt[id].Before(olderThan) {
		return nil
	}
	delete(s.rewindOperations, id)
	delete(s.rewindCreatedAt, id)
	return nil
}
