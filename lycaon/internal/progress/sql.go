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
	database   db.Handle
	queries    *db.Queries
	transcript TranscriptTransactions
}

var _ Store = (*SQLStore)(nil)

type directTranscriptTransactions struct {
	database db.Handle
}

func (d directTranscriptTransactions) AppendMessagesWith(ctx context.Context, _ string, mutate func(*sql.Tx) ([]api.Message, error)) error {
	return db.InTx(ctx, d.database, nil, nil, func(_ *db.Queries, tx *sql.Tx) error {
		_, err := mutate(tx)
		return err
	})
}

func NewSQLStore(database db.Handle, transcript ...TranscriptTransactions) *SQLStore {
	var tx TranscriptTransactions
	if len(transcript) > 0 && transcript[0] != nil {
		tx = transcript[0]
	} else if database != nil {
		tx = directTranscriptTransactions{database: database}
	}
	return &SQLStore{database: database, queries: db.New(database), transcript: tx}
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
