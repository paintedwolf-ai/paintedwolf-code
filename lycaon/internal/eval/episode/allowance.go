package episode

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lycaon/lycaon/internal/session/store"
)

func ReadAllowance(ctx context.Context, database *sql.DB, sessionID string) (store.ModelLimit, error) {
	var limit store.ModelLimit
	var exhausted sql.NullString
	err := database.QueryRowContext(ctx, `SELECT session_id,response_limit,exhausted_attempt_id,
 (SELECT COUNT(*) FROM model_outputs WHERE session_id=? AND scripted=0)
 FROM session_model_limits WHERE session_id=?`, sessionID, sessionID).Scan(&limit.SessionID, &limit.Limit, &exhausted, &limit.Completed)
	limit.ExhaustedAttemptID = exhausted.String
	if err == nil && (limit.Completed > limit.Limit || limit.Limit < 1 || (exhausted.Valid && limit.Completed != limit.Limit)) {
		err = fmt.Errorf("invalid response allowance evidence")
	}
	return limit, err
}
