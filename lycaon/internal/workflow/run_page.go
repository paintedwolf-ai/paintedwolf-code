package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/pagecursor"
	"github.com/lycaon/lycaon/pkg/api"
)

var workflowRunPages = pagecursor.For[workflowRunPageCursor]("workflow_runs")

// ErrInvalidRunPageCursor marks a cursor that cannot belong to this query. It
// wraps the pagecursor.ErrInvalid or pagecursor.ErrExpired decode failure.
var ErrInvalidRunPageCursor = errors.New("invalid workflow run page cursor")

type workflowRunPageCursor struct {
	WatermarkOrdinal int64  `json:"watermark_ordinal"`
	BeforeCreatedAt  string `json:"before_created_at"`
	BeforeID         string `json:"before_id"`
}

// ListPageBySession returns a stable newest-first run page.
func (s *SQLStore) ListPageBySession(ctx context.Context, sessionID string, limit int, statusFilter []string, rawCursor string) (api.WorkflowRunPage, error) {
	limit = boundedRunPageLimit(limit)
	statuses := normalizedRunStatuses(statusFilter)
	cursor, err := decodeWorkflowRunPageCursor(rawCursor, sessionID, statuses)
	if err != nil {
		return api.WorkflowRunPage{}, err
	}
	if cursor.WatermarkOrdinal == 0 {
		cursor.WatermarkOrdinal, err = s.queries.WorkflowRunPageWatermark(ctx)
		if err != nil {
			return api.WorkflowRunPage{}, err
		}
	}
	statusesJSON, err := json.Marshal(statuses)
	if err != nil {
		return api.WorkflowRunPage{}, fmt.Errorf("marshal workflow page statuses: %w", err)
	}
	rows, err := s.queries.PageWorkflowRunsBySession(ctx, db.PageWorkflowRunsBySessionParams{
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

func boundedRunPageLimit(limit int) int {
	if limit <= 0 {
		return 20
	}
	if limit > 100 {
		return 100
	}
	return limit
}

func normalizedRunStatuses(statuses []string) []string {
	out := make([]string, 0, len(statuses))
	seen := make(map[string]struct{}, len(statuses))
	for _, status := range statuses {
		if status = strings.TrimSpace(status); status != "" {
			if _, exists := seen[status]; exists {
				continue
			}
			seen[status] = struct{}{}
			out = append(out, status)
		}
	}
	sort.Strings(out)
	return out
}

func decodeWorkflowRunPageCursor(raw, sessionID string, statuses []string) (workflowRunPageCursor, error) {
	if strings.TrimSpace(raw) == "" {
		return workflowRunPageCursor{}, nil
	}
	cursor, err := workflowRunPages.Decode(raw, workflowRunPageScope(sessionID, statuses))
	if err != nil {
		return workflowRunPageCursor{}, fmt.Errorf("%w: %w", ErrInvalidRunPageCursor, err)
	}
	if cursor.WatermarkOrdinal <= 0 {
		return workflowRunPageCursor{}, fmt.Errorf("%w: %w", ErrInvalidRunPageCursor, pagecursor.ErrInvalid)
	}
	return cursor, nil
}

func encodeWorkflowRunPageCursor(cursor workflowRunPageCursor, sessionID string, statuses []string) (string, error) {
	return workflowRunPages.Encode(workflowRunPageScope(sessionID, statuses), cursor)
}

func workflowRunPageScope(sessionID string, statuses []string) string {
	return pagecursor.Scope(append([]string{sessionID}, statuses...)...)
}

func workflowRunsFromRows(rows []db.WorkflowRuns) ([]api.WorkflowRun, error) {
	out := make([]api.WorkflowRun, 0, len(rows))
	for _, row := range rows {
		run, err := runFromRow(row)
		if err != nil {
			return nil, err
		}
		out = append(out, *run)
	}
	return out, nil
}
