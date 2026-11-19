package sourceledger

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/sourcebranch"
)

// ChatFileRelation is how a chat came to hold an outside file.
type ChatFileRelation int

const (
	ChatFileChanged ChatFileRelation = 1 << iota
	ChatFileRead
	ChatFileOpened
)

// The outside root is stable for the project's lifetime.
func (s *Store) outsideRootID(ctx context.Context, q *db.Queries, projectID string) (string, error) {
	if id, ok := s.outsideRoots.Load(projectID); ok {
		return id.(string), nil
	}
	id, err := q.GetProjectOutsideRootID(ctx, projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	s.outsideRoots.Store(projectID, id)
	return id, nil
}

// Outside worker effects land in the chat's workspace and keep the worker job ID.
func (s *Store) outsideOperationSession(ctx context.Context, q *db.Queries, in RecordInput) (string, error) {
	if in.JobID == "" || in.SessionID == "" || in.BranchID.IsWorker() {
		return in.SessionID, nil
	}
	outsideID, err := s.outsideRootID(ctx, q, in.ProjectID)
	if err != nil || outsideID == "" || (in.RootID != outsideID && in.FromRootID != outsideID) {
		return in.SessionID, err
	}
	chatID, err := q.GetRootSessionID(ctx, in.SessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return in.SessionID, nil
	}
	return chatID, err
}

func (s *Store) referenceMutationChatFile(ctx context.Context, q *db.Queries, in RecordInput, fileID string) error {
	if err := s.referenceChatFile(ctx, q, in.ProjectID, in.RootID, in.SessionID, fileID, ChatFileChanged, in.TS); err != nil {
		return err
	}
	if in.FromRootID != "" && in.FromRootID != in.RootID {
		return s.referenceChatFile(ctx, q, in.ProjectID, in.FromRootID, in.SessionID, fileID, ChatFileChanged, in.TS)
	}
	return nil
}

// A worker's outside file reference belongs to its root chat.
func (s *Store) referenceChatFile(ctx context.Context, q *db.Queries, projectID, rootID, sessionID, fileID string, relation ChatFileRelation, ts time.Time) error {
	if sessionID == "" || fileID == "" {
		return nil
	}
	outside, err := s.outsideRootID(ctx, q, projectID)
	if err != nil || outside == "" || outside != rootID {
		return err
	}
	chat, err := q.GetRootSessionID(ctx, sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	flag := func(r ChatFileRelation) int64 {
		if relation&r != 0 {
			return 1
		}
		return 0
	}
	return q.RecordSourceChatFile(ctx, db.RecordSourceChatFileParams{
		SessionID: chat, FileID: fileID,
		Changed: flag(ChatFileChanged), Read: flag(ChatFileRead), Opened: flag(ChatFileOpened),
		Ts: ts.UTC().Format(time.RFC3339Nano),
	})
}

// OutsideFileHeld checks chat references to the current file at rel.
func (s *Store) OutsideFileHeld(ctx context.Context, projectID, rel string) (bool, error) {
	outside, err := s.outsideRootID(ctx, s.queries, projectID)
	if err != nil || outside == "" {
		return false, err
	}
	head, err := s.queries.GetSourceBranchHeadByPath(ctx, db.GetSourceBranchHeadByPathParams{ProjectID: projectID, BranchID: "", RootID: outside, Path: rel})
	fileID := head.FileID
	if errors.Is(err, sql.ErrNoRows) {
		deleted, deleteErr := s.History.ResolveDeletedPath(ctx, projectID, sourcebranch.Trunk, outside, rel)
		if errors.Is(deleteErr, ErrHistoryNotFound) {
			return false, nil
		}
		fileID, err = deleted.FileID, deleteErr
	}
	if err != nil {
		return false, err
	}
	referenced, err := s.queries.SourceFileReferencedInProject(ctx, db.SourceFileReferencedInProjectParams{FileID: fileID, ProjectID: projectID})
	return referenced != 0, err
}
