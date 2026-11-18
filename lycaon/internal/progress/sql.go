package progress

import (
	"context"
	"database/sql"
	"github.com/lycaon/lycaon/internal/db"
	api "github.com/lycaon/lycaon/pkg/api"
)

// TranscriptTransactions commits derived messages with their source mutation.
type TranscriptTransactions interface {
	AppendMessagesWith(context.Context, string, func(*sql.Tx) ([]api.Message, error)) error
}

type SQLStore struct {
	queries    *db.Queries
	transcript TranscriptTransactions
}

var _ Store = (*SQLStore)(nil)

func NewSQLStore(database db.Handle, transcript TranscriptTransactions) *SQLStore {
	return &SQLStore{queries: db.New(database), transcript: transcript}
}

func (s *SQLStore) Set(sessionID, content string) error {
	_, err := s.Update(context.Background(), sessionID, func(Snapshot) (Document, error) {
		return Document{Content: content, ItemIDs: mintItemIDs(clampContent(content))}, nil
	})
	return err
}

func (s *SQLStore) Get(ctx context.Context, sessionID string) string {
	snapshot, err := s.Snapshot(ctx, sessionID)
	if err != nil {
		return ""
	}
	return snapshot.Content
}
