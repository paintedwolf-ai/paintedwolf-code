package episode

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/lycaon/lycaon/internal/hitl"
	workflowpersistence "github.com/lycaon/lycaon/internal/workflow/persistence"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
)

func readBoundaries(ctx context.Context, database *sql.DB, id string) ([]api.CheckpointEvent, []api.WorkflowRun, error) {
	rows, err := hitl.NewSQLStore(database).ListBySession(ctx, id, "", nil)
	if err != nil {
		return nil, nil, err
	}
	cards := make([]api.CheckpointEvent, 0, len(rows))
	for _, row := range rows {
		card := hitl.StoredCheckpointToEvent(row)
		if row.Kind == api.CheckpointKindToolApproval && card.ToolApproval == nil {
			return nil, nil, fmt.Errorf("checkpoint %s has no valid approval plan", row.ID)
		}
		cards = append(cards, card)
	}
	runs, err := readRuns(ctx, workflowpersistence.New(database), id)
	return cards, runs, err
}

func readRuns(ctx context.Context, store *runstate.Repository, sessionID string) ([]api.WorkflowRun, error) {
	runs := []api.WorkflowRun{}
	cursor := ""
	for {
		page, err := store.Runs.ListPageBySession(ctx, sessionID, 100, nil, cursor)
		if err != nil {
			return nil, err
		}
		runs = append(runs, page.Runs...)
		if page.NextCursor == "" {
			return runs, nil
		}
		if page.NextCursor == cursor {
			return nil, fmt.Errorf("workflow history cursor did not advance")
		}
		cursor = page.NextCursor
	}
}

// SourceEffect binds a committed path change to its writer and branch.
type SourceEffect struct {
	SessionID   string `json:"session_id"`
	JobID       string `json:"job_id"`
	ToolCallID  string `json:"tool_call_id"`
	ToolName    string `json:"tool_name"`
	CommittedAt string `json:"committed_ts"`
	BranchID    string `json:"branch_id"`
	Origin      string `json:"origin"`
	Path        string `json:"path"`
	EntryKind   string `json:"entry_kind"`
	Operation   string `json:"op"`
}

func readEffects(ctx context.Context, database *sql.DB, project string) ([]SourceEffect, error) {
	rows, err := database.QueryContext(ctx, `SELECT o.session_id,o.job_id,o.tool_call_id,o.tool_name,o.committed_ts,
 o.branch_id,o.origin,e.path,e.entry_kind,e.op FROM source_operations o
 JOIN source_effects e ON e.operation_id=o.id WHERE o.project_id=? ORDER BY o.committed_ts,e.ordinal`, project)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	effects := []SourceEffect{}
	for rows.Next() {
		var effect SourceEffect
		if err := rows.Scan(&effect.SessionID, &effect.JobID, &effect.ToolCallID, &effect.ToolName, &effect.CommittedAt,
			&effect.BranchID, &effect.Origin, &effect.Path, &effect.EntryKind, &effect.Operation); err != nil {
			return nil, err
		}
		effects = append(effects, effect)
	}
	return effects, rows.Err()
}

func readPromotions(ctx context.Context, database *sql.DB, project string) (map[string]string, error) {
	rows, err := database.QueryContext(ctx, `SELECT l.worker_job_id,l.created_at FROM landed_changes l
 JOIN worker_jobs j ON j.id=l.worker_job_id WHERE j.project_id=? ORDER BY l.created_at`, project)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	promotions := map[string]string{}
	for rows.Next() {
		var job, at string
		if err := rows.Scan(&job, &at); err != nil {
			return nil, err
		}
		promotions[job] = at
	}
	return promotions, rows.Err()
}
