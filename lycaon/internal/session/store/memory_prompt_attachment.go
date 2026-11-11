package store

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ListOperationPromptAttachmentRetentions returns one operation's claims.
func (a *memoryAttachmentBlobs) ListOperationPromptAttachmentRetentions(_ context.Context, operationID string) ([]PromptAttachmentRetention, error) {
	return listPromptAttachmentRetentions(a.core, "", operationID, ""), nil
}

// PromptAttachmentBlobRetained reports whether durable history claims a blob.
func (a *memoryAttachmentBlobs) PromptAttachmentBlobRetained(_ context.Context, projectID, blobID string) (bool, error) {
	s := a.core
	s.mu.RLock()
	defer s.mu.RUnlock()
	return promptAttachmentBlobRetainedLocked(s, projectID, blobID), nil
}

// RecordPromptAttachmentBlob records one materialized body.
func (a *memoryAttachmentBlobs) RecordPromptAttachmentBlob(_ context.Context, projectID, blobID string, byteSize int64) error {
	s := a.core
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
func (a *memoryAttachmentBlobs) DeletePromptAttachmentBlob(_ context.Context, projectID, blobID string) error {
	s := a.core
	s.mu.Lock()
	defer s.mu.Unlock()
	projectID = strings.TrimSpace(projectID)
	blobID = strings.TrimSpace(blobID)
	if promptAttachmentBlobRetainedLocked(s, projectID, blobID) {
		return ErrPromptAttachmentRetained
	}
	delete(s.promptAttachmentBlobs[projectID], blobID)
	return nil
}

func (a *memoryAttachmentBlobs) ListPromptAttachmentReclaimCandidates(
	_ context.Context, projectID string, createdBefore time.Time, limit int,
) ([]string, error) {
	if limit <= 0 {
		return nil, nil
	}
	s := a.core
	s.mu.RLock()
	defer s.mu.RUnlock()
	type candidate struct {
		id        string
		createdAt time.Time
	}
	rows := make([]candidate, 0)
	projectID = strings.TrimSpace(projectID)
	for blobID, blob := range s.promptAttachmentBlobs[projectID] {
		if blob.createdAt.Before(createdBefore) && !promptAttachmentBlobRetainedLocked(s, projectID, blobID) {
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
func (a *memoryAttachmentBlobs) PromptAttachmentStorageUsage(_ context.Context, projectID string) (int64, error) {
	s := a.core
	s.mu.RLock()
	defer s.mu.RUnlock()
	var total int64
	for _, blob := range s.promptAttachmentBlobs[strings.TrimSpace(projectID)] {
		total += blob.byteSize
	}
	return total, nil
}

func listPromptAttachmentRetentions(s *memoryCore, projectFilter, operationFilter, blobFilter string) []PromptAttachmentRetention {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return listPromptAttachmentRetentionsLocked(s, projectFilter, operationFilter, blobFilter)
}

func promptAttachmentBlobRetainedLocked(s *memoryCore, projectID, blobID string) bool {
	return len(listPromptAttachmentRetentionsLocked(
		s, strings.TrimSpace(projectID), "", strings.TrimSpace(blobID),
	)) != 0
}

func listPromptAttachmentRetentionsLocked(s *memoryCore, projectFilter, operationFilter, blobFilter string) []PromptAttachmentRetention {
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
