package cost

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// Summary reads compact lifetime totals shared with project reporting.
func (t *SQLTracker) Summary(ctx context.Context, scope api.CostScope, sessionID, projectID string) (api.CostSummary, error) {
	var selectSQL, arg string
	switch scope {
	case api.CostScopeProject:
		arg = strings.TrimSpace(projectID)
		selectSQL = `SELECT '@project' AS bucket, '' AS root_id, * FROM llm_cost_totals WHERE project_id = ?1`
	case api.CostScopeSession:
		arg = strings.TrimSpace(sessionID)
		selectSQL = `SELECT ?1 AS bucket, ?1 AS root_id, * FROM llm_cost_totals WHERE session_id = ?1 OR parent_session_id = ?1`
	default:
		return api.CostSummary{}, fmt.Errorf("unsupported cost scope: %s", scope)
	}
	if arg == "" {
		return api.CostSummary{}, fmt.Errorf("%s scope requires an id", scope)
	}
	// #nosec G202 -- selectSQL is one of the closed scope queries above.
	rows, err := t.db.QueryContext(ctx, `WITH buckets AS MATERIALIZED (`+selectSQL+`)`+costBucketSummarySQL, arg)
	if err != nil {
		return api.CostSummary{}, err
	}
	summaries, err := readCostSummaryRows(rows)
	if err != nil {
		return api.CostSummary{}, err
	}
	key := "@project"
	if scope == api.CostScopeSession {
		key = arg
	}
	summary := reportSummary(summaries, key, projectID)
	summary.Scope = scope
	if scope == api.CostScopeSession {
		summary.SessionID = arg
	}
	return summary, nil
}
