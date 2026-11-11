package invocation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// ActivityTransition identifies structural state changes without loading program history.
func (r *SQLRecorder) ActivityTransition(ctx context.Context, execution string) (string, error) {
	var node, operation, scope, state, code, hostCode string
	var status int
	err := r.database.QueryRowContext(ctx, `SELECT node_id,operation_id,scope_json,state,COALESCE(status_code,0),stop_code,host_stop_code FROM invocation_program_executions WHERE id=?`, execution).Scan(&node, &operation, &scope, &state, &status, &code, &hostCode)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal([]any{node, operation, scope, state, status, code, hostCode})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
