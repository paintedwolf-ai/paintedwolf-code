// Package commandinvoke stores contribution command admission state.
package commandinvoke

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lycaon/lycaon/internal/contribframe"
	"github.com/lycaon/lycaon/internal/contribution"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/pkg/api"
)

// Input is one structured invocation context.
type Input struct {
	Frame     *contribframe.Frame
	CommandID contribution.ID
	Command   *contribution.Command
	Resolved  contribution.ResolvedCommand
	ProjectID string
	SessionID string
	Context   api.CommandInvokeContext
	Args      map[string]any
}

// Authority validates one invocation against machine state.
type Authority interface {
	Authorize(ctx context.Context, in Input) error
}

// Receipt is one stored invocation outcome.
type Receipt struct {
	OperationID  string
	InputDigest  string
	ResponseJSON string
}

// Receipts stores idempotent invocation outcomes.
type Receipts interface {
	Load(ctx context.Context, operationID string) (Receipt, bool, error)
	Save(ctx context.Context, receipt Receipt) error
}

// SQLReceipts stores receipts in the host database.
type SQLReceipts struct {
	DB db.Handle
}

// Load implements Receipts.
func (s SQLReceipts) Load(ctx context.Context, operationID string) (Receipt, bool, error) {
	row := s.DB.QueryRowContext(ctx,
		`SELECT operation_id, input_digest, response_json FROM command_invocations WHERE operation_id = ?`,
		operationID)
	var out Receipt
	if err := row.Scan(&out.OperationID, &out.InputDigest, &out.ResponseJSON); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Receipt{}, false, nil
		}
		return Receipt{}, false, err
	}
	return out, true, nil
}

// Save implements Receipts.
func (s SQLReceipts) Save(ctx context.Context, receipt Receipt) error {
	_, err := s.DB.ExecContext(ctx,
		`INSERT INTO command_invocations (operation_id, input_digest, response_json, created_at)
		 VALUES (?, ?, ?, ?)`,
		receipt.OperationID, receipt.InputDigest, receipt.ResponseJSON,
		time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

// RequestDigest identifies an invocation for replay.
func RequestDigest(route, scopeID, commandID string, req api.CommandInvokeRequest) (string, error) {
	payload, err := json.Marshal(struct {
		Route     string                   `json:"route"`
		ScopeID   string                   `json:"scope_id"`
		CommandID string                   `json:"command_id"`
		Request   api.CommandInvokeRequest `json:"request"`
	}{route, scopeID, commandID, req})
	if err != nil {
		return "", fmt.Errorf("encode command invocation digest: %w", err)
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

// EncodeResponse serializes one response for receipt storage.
func EncodeResponse(status int, body api.CommandInvokeResponse) (string, error) {
	payload, err := json.Marshal(struct {
		Status int                       `json:"status"`
		Body   api.CommandInvokeResponse `json:"body"`
	}{status, body})
	if err != nil {
		return "", fmt.Errorf("encode invoke receipt: %w", err)
	}
	return string(payload), nil
}

// DecodeResponse restores a stored response.
func DecodeResponse(raw string) (int, api.CommandInvokeResponse, error) {
	var decoded struct {
		Status int                       `json:"status"`
		Body   api.CommandInvokeResponse `json:"body"`
	}
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return 0, api.CommandInvokeResponse{}, fmt.Errorf("decode invoke receipt: %w", err)
	}
	return decoded.Status, decoded.Body, nil
}
