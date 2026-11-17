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

// referenceChatFile records that the root chat of sessionID holds an outside
// file. Files under attached roots need no reference.
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
