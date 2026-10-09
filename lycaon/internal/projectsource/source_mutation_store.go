package projectsource

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/internal/people/peoplestore"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourceledger"
)

type SourceMutationJournal struct {
	db      db.Handle
	people  *peoplestore.Store
	stateMu sync.Mutex
	memory  map[string]*sourceMutationRow
}

func (s *SourceMutationJournal) load(ctx context.Context, id string) (*sourceMutationRow, bool, error) {
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

func (s *SourceMutationJournal) insert(ctx context.Context, row *sourceMutationRow) error {
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

func (s *SourceMutationJournal) update(ctx context.Context, row *sourceMutationRow) error {
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

func (s *SourceMutationJournal) pending(ctx context.Context) ([]*sourceMutationRow, error) {
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

// ObservationScope reads the same durable plans used to recover publication.
// Completed or failed-before-application operations cannot suppress observations.
func (s *SourceMutationJournal) ObservationScope(ctx context.Context, tx *sql.Tx, projectID string) (sourceledger.MutationObservationScope, error) {
	rows, err := tx.QueryContext(ctx, `SELECT plan_json FROM source_mutations WHERE project_id=? AND status IN (?,?)`, projectID, sourceMutationPrepared, sourceMutationFileApplied)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var pending pendingMutationObservations
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var plan sourceMutationPlan
		if err := json.Unmarshal([]byte(raw), &plan); err != nil {
			return nil, fmt.Errorf("decode pending source mutation: %w", err)
		}
		pending = append(pending, plan.observationPaths()...)
	}
	return pending, rows.Err()
}

type pendingMutationPath struct {
	branch sourcebranch.ID
	rootID string
	path   string
}

type pendingMutationObservations []pendingMutationPath

func (paths pendingMutationObservations) Pending(branch sourcebranch.ID, rootID, path string) bool {
	for _, pending := range paths {
		if pending.branch.String() == branch.String() && pending.rootID == rootID && overlappingSourcePaths(pending.path, path) {
			return true
		}
	}
	return false
}

func (p sourceMutationPlan) observationPaths() []pendingMutationPath {
	if !p.Changed {
		return nil
	}
	var paths []pendingMutationPath
	for _, in := range p.ledgerInputs("") {
		if in.Path != "" {
			paths = append(paths, pendingMutationPath{in.BranchID, in.RootID, in.Path})
		}
		fromRoot := in.FromRootID
		if fromRoot == "" {
			fromRoot = in.RootID
		}
		if in.FromPath != "" {
			paths = append(paths, pendingMutationPath{in.BranchID, fromRoot, in.FromPath})
		}
	}
	return paths
}

// CommittedSourceResult reads the authoritative filesystem journal after recovery.
func (s *SourceMutationJournal) CommittedSourceResult(ctx context.Context, id string) (json.RawMessage, bool, error) {
	row, found, err := s.load(ctx, id)
	if err != nil || !found {
		return nil, false, err
	}
	return row.Response, row.Status == sourceMutationCommitted, nil
}
