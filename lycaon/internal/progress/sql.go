package progress

import (
	"context"
	"database/sql"

	"github.com/lycaon/lycaon/internal/db"
	api "github.com/lycaon/lycaon/pkg/api"
)

// TranscriptTransactions commits derived messages inside the same transaction as mutate.
type TranscriptTransactions interface {
	AppendMessagesWith(context.Context, string, func(*sql.Tx) ([]api.Message, error)) error
}

// SQLStore persists session_progress and session_progress_items in SQLite.
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

// NewSQLStore constructs a SQLite-backed progress store.
func NewSQLStore(database db.Handle, transcript ...TranscriptTransactions) *SQLStore {
	var tx TranscriptTransactions
	if len(transcript) > 0 && transcript[0] != nil {
		tx = transcript[0]
	} else if database != nil {
		tx = directTranscriptTransactions{database: database}
	}
	return &SQLStore{database: database, queries: db.New(database), transcript: tx}
}
