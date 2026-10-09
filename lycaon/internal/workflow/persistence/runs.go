package persistence

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
	"strings"
)

type Runs struct {
	transactions *Transactions
}

func (s *Runs) Get(ctx context.Context, id string) (*api.WorkflowRun, error) {
	row, err := s.transactions.queries.GetWorkflowRun(ctx, id)
	if db.IsNoRows(err) {
		return nil, runstate.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return runFromRow(row)
}

func (s *Runs) ActiveBySession(ctx context.Context, sessionID string) (*api.WorkflowRun, error) {
	row, err := s.transactions.queries.ActiveWorkflowRunBySession(ctx, sessionID)
	if db.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return runFromRow(row)
}

func (s *Runs) ActiveStateBySession(ctx context.Context, sessionID string) (*api.WorkflowRun, map[string]any, error) {
	row, err := s.transactions.queries.ActiveWorkflowRunBySession(ctx, sessionID)
	if db.IsNoRows(err) {
		return nil, map[string]any{}, nil
	}
	if err != nil {
		return nil, nil, err
	}
	run, err := runFromRow(row)
	if err != nil {
		return nil, nil, err
	}
	vars, err := scaffoldVarsFromJSON(row.VarsJson)
	if err != nil {
		return nil, nil, err
	}
	return run, vars, nil
}

func (s *Runs) GetScaffoldVars(ctx context.Context, workflowRunID string) (map[string]any, error) {
	raw, err := s.transactions.queries.GetScaffoldVars(ctx, workflowRunID)
	if db.IsNoRows(err) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	return scaffoldVarsFromJSON(raw)
}

func (s *Runs) ListBySession(ctx context.Context, sessionID string, limit int, statusFilter []string) ([]api.WorkflowRun, error) {
	return s.listBySession(ctx, s.transactions.queries, sessionID, limit, statusFilter)
}

func (s *Runs) ListRunning(ctx context.Context) ([]api.WorkflowRun, error) {
	rows, err := s.transactions.queries.ListRunningWorkflowRuns(ctx)
	if err != nil {
		return nil, err
	}
	runs := make([]api.WorkflowRun, 0, len(rows))
	for _, row := range rows {
		run, mapErr := runFromRow(row)
		if mapErr != nil {
			return nil, mapErr
		}
		runs = append(runs, *run)
	}
	return runs, nil
}

func (s *Runs) ListPausedOnChild(ctx context.Context) ([]api.WorkflowRun, error) {
	rows, err := s.transactions.queries.ListPausedOnChildWorkflowRuns(ctx)
	if err != nil {
		return nil, err
	}
	runs := make([]api.WorkflowRun, 0, len(rows))
	for _, row := range rows {
		run, mapErr := runFromRow(row)
		if mapErr != nil {
			return nil, mapErr
		}
		runs = append(runs, *run)
	}
	return runs, nil
}

func (s *Runs) listBySession(ctx context.Context, queries *db.Queries, sessionID string, limit int, statusFilter []string) ([]api.WorkflowRun, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	statuses := make([]string, 0, len(statusFilter))
	for _, st := range statusFilter {
		statuses = append(statuses, strings.TrimSpace(st))
	}
	var rows []db.WorkflowRuns
	var err error
	if len(statuses) > 0 {
		rows, err = queries.ListWorkflowRunsBySessionWithStatus(ctx, db.ListWorkflowRunsBySessionWithStatusParams{
			SessionID: sessionID,
			Statuses:  statuses,
			Limit:     int64(limit),
		})
	} else {
		rows, err = queries.ListWorkflowRunsBySession(ctx, db.ListWorkflowRunsBySessionParams{
			SessionID: sessionID,
			Limit:     int64(limit),
		})
	}
	if err != nil {
		return nil, err
	}
	out := make([]api.WorkflowRun, 0, len(rows))
	for _, r := range rows {
		run, err := runFromRow(r)
		if err != nil {
			return nil, err
		}
		out = append(out, *run)
	}
	return out, nil
}

func (s *Runs) ActiveByProjectForBlueprint(ctx context.Context, projectID, blueprintPath string) (*api.WorkflowRun, error) {
	projectID = strings.TrimSpace(projectID)
	blueprintPath = strings.TrimSpace(blueprintPath)
	if projectID == "" || blueprintPath == "" {
		return nil, nil
	}
	row, err := s.transactions.queries.ActiveWorkflowRunByProjectAndBlueprintPath(ctx, db.ActiveWorkflowRunByProjectAndBlueprintPathParams{
		ProjectID:     projectID,
		BlueprintPath: db.NullString(blueprintPath),
	})
	if db.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return runFromRow(row)
}

func (s *Runs) LatestChildByParentRunID(ctx context.Context, parentRunID string) (*api.WorkflowRun, error) {
	parentRunID = strings.TrimSpace(parentRunID)
	if parentRunID == "" {
		return nil, nil
	}
	row, err := s.transactions.queries.LatestChildWorkflowRun(ctx, db.NullString(parentRunID))
	if db.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return runFromRow(row)
}

func (s *Runs) ListPageBySession(ctx context.Context, sessionID string, limit int, statusFilter []string, rawCursor string) (api.WorkflowRunPage, error) {
	limit = boundedRunPageLimit(limit)
	statuses := normalizedRunStatuses(statusFilter)
	cursor, err := decodeWorkflowRunPageCursor(rawCursor, sessionID, statuses)
	if err != nil {
		return api.WorkflowRunPage{}, err
	}
	if cursor.WatermarkOrdinal == 0 {
		cursor.WatermarkOrdinal, err = s.transactions.queries.WorkflowRunPageWatermark(ctx)
		if err != nil {
			return api.WorkflowRunPage{}, err
		}
	}
	statusesJSON, err := json.Marshal(statuses)
	if err != nil {
		return api.WorkflowRunPage{}, fmt.Errorf("marshal workflow page statuses: %w", err)
	}
	rows, err := s.transactions.queries.PageWorkflowRunsBySession(ctx, db.PageWorkflowRunsBySessionParams{
		SessionID: sessionID, StatusesJson: string(statusesJSON), PageLimit: int64(limit + 1),
		WatermarkOrdinal: cursor.WatermarkOrdinal,
		BeforeCreatedAt:  cursor.BeforeCreatedAt, BeforeID: cursor.BeforeID,
	})
	if err != nil {
		return api.WorkflowRunPage{}, err
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	runs, err := workflowRunsFromRows(rows)
	if err != nil {
		return api.WorkflowRunPage{}, err
	}
	page := api.WorkflowRunPage{Runs: runs}
	if hasMore && len(rows) > 0 {
		last := rows[len(rows)-1]
		cursor.BeforeCreatedAt = last.CreatedAt
		cursor.BeforeID = last.ID
		page.NextCursor, err = encodeWorkflowRunPageCursor(cursor, sessionID, statuses)
		if err != nil {
			return api.WorkflowRunPage{}, err
		}
	}
	return page, nil
}
