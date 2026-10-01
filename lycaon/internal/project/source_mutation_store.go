package project

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/people"
)

func (s *SourceMutationService) load(ctx context.Context, id string) (*sourceMutationRow, bool, error) {
	if s.db == nil {
		s.stateMu.Lock()
		defer s.stateMu.Unlock()
		row, ok := s.memory[id]
		if !ok {
			return nil, false, nil
		}
		copy := *row
		return &copy, true, nil
	}
	row := &sourceMutationRow{}
	var planJSON, response sql.NullString
	var status, createdAt, updatedAt string
	err := s.db.QueryRowContext(ctx, `SELECT id, project_id, kind, input_digest, plan_json, status, response_json, error, created_at, updated_at FROM source_mutations WHERE id=?`, id).
		Scan(&row.ID, &row.ProjectID, &row.Kind, &row.InputDigest, &planJSON, &status, &response, &row.Error, &createdAt, &updatedAt)
	if db.IsNoRows(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if err := json.Unmarshal([]byte(planJSON.String), &row.Plan); err != nil {
		return nil, false, err
	}
	row.Status, row.Response = sourceMutationStatus(status), json.RawMessage(response.String)
	row.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	row.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	return row, true, nil
}

func (s *SourceMutationService) insert(ctx context.Context, row *sourceMutationRow) error {
	if s.db == nil {
		s.stateMu.Lock()
		defer s.stateMu.Unlock()
		copy := *row
		s.memory[row.ID] = &copy
		return nil
	}
	if row.Plan.AgentEffect == nil && row.Plan.Agent == nil {
		person, err := people.Acting(ctx, s.people)
		if err != nil {
			return fmt.Errorf("source mutation person: %w", err)
		}
		row.Plan.PersonID = person.ID
	}
	planJSON, err := json.Marshal(row.Plan)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO source_mutations(id, project_id, kind, input_digest, plan_json, status, error, created_at, updated_at) VALUES(?,?,?,?,?,?,?,?,?)`,
		row.ID, row.ProjectID, row.Kind, row.InputDigest, string(planJSON), row.Status, row.Error,
		row.CreatedAt.Format(time.RFC3339Nano), row.UpdatedAt.Format(time.RFC3339Nano))
	return err
}

func (s *SourceMutationService) update(ctx context.Context, row *sourceMutationRow) error {
	row.UpdatedAt = time.Now().UTC()
	if s.db == nil {
		s.stateMu.Lock()
		defer s.stateMu.Unlock()
		copy := *row
		s.memory[row.ID] = &copy
		return nil
	}
	planJSON, err := json.Marshal(row.Plan)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE source_mutations SET status=?, response_json=?, error=?, updated_at=?, plan_json=? WHERE id=?`,
		row.Status, nullableSourceResponse(row.Response), row.Error, row.UpdatedAt.Format(time.RFC3339Nano), string(planJSON), row.ID)
	return err
}

func nullableSourceResponse(response json.RawMessage) any {
	if len(response) == 0 {
		return nil
	}
	return string(response)
}

func (s *SourceMutationService) pending(ctx context.Context) ([]*sourceMutationRow, error) {
	if s.db == nil {
		s.stateMu.Lock()
		defer s.stateMu.Unlock()
		rows := make([]*sourceMutationRow, 0)
		for _, row := range s.memory {
			if row.Status == sourceMutationPrepared || row.Status == sourceMutationFileApplied {
				copy := *row
				rows = append(rows, &copy)
			}
		}
		return rows, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM source_mutations WHERE status IN ('prepared','file_applied') ORDER BY created_at,id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	out := make([]*sourceMutationRow, 0, len(ids))
	for _, id := range ids {
		row, found, err := s.load(ctx, id)
		if err != nil {
			return nil, err
		}
		if found {
			out = append(out, row)
		}
	}
	return out, nil
}
