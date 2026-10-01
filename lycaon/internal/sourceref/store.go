package sourceref

import (
	"context"
	"database/sql"
	"errors"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/pkg/api"
)

// ReadContext follows the original message identity already held by search.
func ReadContext(ctx context.Context, database db.DBTX, sessionID, messageID string) (*api.SourceContext, error) {
	if sessionID == "" || messageID == "" {
		return nil, nil
	}
	var raw sql.NullString
	if err := database.QueryRowContext(ctx, `SELECT navigation_refs_json FROM messages WHERE session_id = ? AND id = ?`, sessionID, messageID).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	metadata, err := DecodeMetadata(raw)
	if err != nil {
		return nil, err
	}
	return &metadata.Context, nil
}
