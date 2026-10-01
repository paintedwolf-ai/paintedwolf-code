package projectremoval

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type record struct {
	ProjectID string
	Request   string
	Result    wire.ProjectRemovalResult
	Settled   bool
}

// Store retains operation identity independently of deleted project rows.
type Store struct {
	database db.Handle
}

func NewStore(database db.Handle) *Store {
	return &Store{database: database}
}

func (s *Store) read(ctx context.Context, id string) (record, bool, error) {
	var row record
	var result string
	err := s.database.QueryRowContext(ctx, `SELECT project_id, request_json, result_json, settled FROM project_removals WHERE operation_id=?`, id).Scan(&row.ProjectID, &row.Request, &result, &row.Settled)
	if errors.Is(err, sql.ErrNoRows) {
		return row, false, nil
	}
	if err != nil {
		return row, false, err
	}
	err = json.Unmarshal([]byte(result), &row.Result)
	return row, err == nil, err
}

func (s *Store) begin(ctx context.Context, row record) error {
	result, err := json.Marshal(row.Result)
	if err != nil {
		return err
	}
	_, err = s.database.ExecContext(ctx, `INSERT INTO project_removals(operation_id, project_id, request_json, result_json, created_at) VALUES(?,?,?,?,?)`, row.Result.OperationID, row.ProjectID, row.Request, string(result), time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) save(ctx context.Context, result wire.ProjectRemovalResult, settled bool) error {
	encoded, err := json.Marshal(result)
	if err != nil {
		return err
	}
	_, err = s.database.ExecContext(ctx, `UPDATE project_removals SET result_json=?, settled=? WHERE operation_id=?`, string(encoded), settled, result.OperationID)
	return err
}
