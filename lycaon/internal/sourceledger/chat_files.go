package sourceledger

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/lycaon/lycaon/internal/db"
)

// ChatFileRelation is how a chat came to hold an outside file.
type ChatFileRelation int

const (
	ChatFileChanged ChatFileRelation = 1 << iota
	ChatFileRead
	ChatFileOpened
)

// outsideRootID names the project's outside namespace, which never changes
// once the project exists.
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

func (s *Store) referenceMutationChatFile(ctx context.Context, q *db.Queries, in RecordInput, fileID string) error {
	if err := s.referenceChatFile(ctx, q, in.ProjectID, in.RootID, in.Path, in.SessionID, fileID, ChatFileChanged, in.TS); err != nil {
		return err
	}
	if in.FromRootID != "" && in.FromRootID != in.RootID {
		return s.referenceChatFile(ctx, q, in.ProjectID, in.FromRootID, in.FromPath, in.SessionID, fileID, ChatFileChanged, in.TS)
	}
	return nil
}

// referenceChatFile links a root chat to an outside file and its location.
func (s *Store) referenceChatFile(ctx context.Context, q *db.Queries, projectID, rootID, rel, sessionID, fileID string, relation ChatFileRelation, ts time.Time) error {
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
	if err := q.RecordSourceChatFile(ctx, db.RecordSourceChatFileParams{
		SessionID: chat, FileID: fileID,
		Changed: flag(ChatFileChanged), Read: flag(ChatFileRead), Opened: flag(ChatFileOpened),
		Ts: ts.UTC().Format(time.RFC3339Nano),
	}); err != nil {
		return err
	}
	if rel == "" {
		return nil
	}
	return placeChatLocation(ctx, q, chat, rel, ts)
}

// OutsideFileHeld reports whether a chat of the project holds the outside
// file currently at rel.
func (s *Store) OutsideFileHeld(ctx context.Context, projectID, rel string) (bool, error) {
	outside, err := s.outsideRootID(ctx, s.queries, projectID)
	if err != nil || outside == "" {
		return false, err
	}
	head, err := s.queries.GetSourceBranchHeadByPath(ctx, db.GetSourceBranchHeadByPathParams{ProjectID: projectID, BranchID: "", RootID: outside, Path: rel})
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	referenced, err := s.queries.SourceFileReferencedInProject(ctx, db.SourceFileReferencedInProjectParams{FileID: head.FileID, ProjectID: projectID})
	return referenced != 0, err
}
