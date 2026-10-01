package episode

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/lycaon/lycaon/internal/session/store"
)

// Execution identifies the application records used for grading.
type Execution struct {
	SessionID         string              `json:"session_id"`
	TurnID            string              `json:"turn_id"`
	Status            string              `json:"status"`
	CloseoutAttemptID string              `json:"closeout_attempt_id"`
	OutputID          string              `json:"output_id"`
	SubmissionIDs     []string            `json:"submission_ids"`
	Closeout          *store.TurnCloseout `json:"closeout,omitempty"`
}

func ReadExecution(ctx context.Context, database *sql.DB, sessionID string) (Execution, error) {
	result := Execution{SessionID: sessionID, SubmissionIDs: []string{}}
	var closeoutAttempt sql.NullString
	var body sql.NullString
	err := database.QueryRowContext(ctx, `SELECT t.id,t.status,h.closeout_attempt_id,c.closeout_json
 FROM session_turn_heads h JOIN turns t ON t.id=h.turn_id
 LEFT JOIN turn_attempt_closeouts c ON c.turn_attempt_id=h.closeout_attempt_id
 WHERE h.session_id=?`, sessionID).Scan(&result.TurnID, &result.Status, &closeoutAttempt, &body)
	if err != nil {
		return result, err
	}
	result.CloseoutAttemptID = closeoutAttempt.String
	if body.Valid {
		var message store.TurnCloseout
		if err := json.Unmarshal([]byte(body.String), &message); err != nil {
			return result, err
		}
		result.Closeout = &message
		result.OutputID = message.OutputID
	}
	rows, err := database.QueryContext(ctx, `SELECT id FROM prompt_submissions WHERE session_id=? AND origin='user' ORDER BY admission_seq`, sessionID)
	if err != nil {
		return result, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return result, err
		}
		result.SubmissionIDs = append(result.SubmissionIDs, id)
	}
	return result, rows.Err()
}
