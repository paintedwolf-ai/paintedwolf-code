package cost

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/pagecursor"
	"github.com/lycaon/lycaon/pkg/api"
)

// ReportQuery selects a bounded, globally ordered page of project chats.
type ReportQuery struct {
	Limit                int
	Cursor, Search, Sort string
}

var ErrInvalidReportQuery = errors.New("invalid project cost query")

var costReportPages = pagecursor.For[reportCursor]("project_cost")

type reportCursor struct {
	Value string `json:"value"`
	ID    string `json:"id"`
}

const reportAttributionSQL = `WITH attributed AS MATERIALIZED (
 SELECT t.*, COALESCE(s.id, p.id, '') AS root_id
 FROM llm_cost_totals t
 LEFT JOIN sessions s ON s.id = t.session_id AND s.project_id = t.project_id AND s.parent_session_id IS NULL
 LEFT JOIN sessions p ON p.id = t.parent_session_id AND p.project_id = t.project_id AND p.parent_session_id IS NULL
 WHERE t.project_id = ?1
)`
const reportSessionTotalsSQL = reportAttributionSQL + `, amounts AS (
 SELECT root_id,
 SUM(estimated_nano_usd) AS nano_usd, SUM(prompt_tokens + completion_tokens) AS tokens,
 COUNT(DISTINCT CASE WHEN caller = 'worker' THEN session_id END) AS workers
 FROM attributed WHERE status = 'reported' AND root_id <> '' AND (
   (caller IN ('', 'coordinator') AND session_id = root_id) OR
   (caller = 'worker' AND parent_session_id = root_id) OR caller = 'summarizer'
 ) GROUP BY root_id
), chats AS (
 SELECT s.id, s.activity_at, s.archived_at,
 COALESCE(a.nano_usd, 0) AS nano_usd, COALESCE(a.tokens, 0) AS tokens, COALESCE(a.workers, 0) AS workers,
 (instr(lower(COALESCE(NULLIF(trim(s.title), ''), 'Untitled session')), lower(?2)) > 0) AS matches
 FROM sessions s LEFT JOIN amounts a ON a.root_id = s.id
 WHERE s.project_id = ?1 AND s.parent_session_id IS NULL
)`

func (t *SQLTracker) ProjectReport(ctx context.Context, projectID string, query ReportQuery) (api.ProjectCostReport, error) {
	projectID, query.Search = strings.TrimSpace(projectID), strings.TrimSpace(query.Search)
	if query.Sort == "" {
		query.Sort = "cost"
	}
	expression, ok := map[string]string{"cost": "nano_usd", "tokens": "tokens", "activity": "activity_at", "id": "id"}[query.Sort]
	if !ok || projectID == "" || len(query.Search) > 500 {
		return api.ProjectCostReport{}, ErrInvalidReportQuery
	}
	if query.Limit <= 0 {
		query.Limit = 40
	}
	query.Limit = min(query.Limit, 200)
	scope := pagecursor.Scope(projectID, query.Search, query.Sort)
	var cursor reportCursor
	if query.Cursor != "" {
		c, err := costReportPages.Decode(query.Cursor, scope)
		if err != nil {
			return api.ProjectCostReport{}, err
		}
		cursor = c
	}
	var tx *sql.Tx
	var err error
	if reader, ok := t.db.(db.ReadHandle); ok {
		tx, err = reader.BeginReadTx(ctx)
	} else {
		tx, err = t.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	}
	if err != nil {
		return api.ProjectCostReport{}, err
	}
	defer func() { _ = tx.Rollback() }()
	report, err := readReportPage(ctx, tx, projectID, query, expression, cursor, scope)
	if err != nil {
		return api.ProjectCostReport{}, err
	}
	ids := make([]string, 0, len(report.Sessions))
	for _, row := range report.Sessions {
		ids = append(ids, row.Session.ID)
	}
	summaries, err := readReportSummaries(ctx, tx, projectID, ids)
	if err != nil {
		return api.ProjectCostReport{}, err
	}
	report.Summary = reportSummary(summaries, "@project", projectID)
	report.ProjectUtilities = reportSummary(summaries, "@utilities", projectID)
	report.RetiredSessions = reportSummary(summaries, "@retired", projectID)
	for i := range report.Sessions {
		id := report.Sessions[i].Session.ID
		summary := reportSummary(summaries, id, projectID)
		summary.Scope, summary.SessionID = api.CostScopeSession, id
		report.Sessions[i].Cost = summary
	}
	return report, tx.Commit()
}

func readReportPage(ctx context.Context, tx *sql.Tx, projectID string, query ReportQuery, expression string, cursor reportCursor, scope string) (api.ProjectCostReport, error) {
	report := api.ProjectCostReport{Sessions: []api.ProjectCostSession{}}
	var maxNano int64
	err := tx.QueryRowContext(ctx, reportSessionTotalsSQL+`
 SELECT COUNT(*), COALESCE(SUM(matches),0), COUNT(archived_at), COALESCE(SUM(workers),0),
 COALESCE(MAX(nano_usd),0), COALESCE(MAX(tokens),0) FROM chats`, projectID, query.Search).Scan(
		&report.SessionCount, &report.Total, &report.ArchivedSessionCount, &report.WorkerTaskCount, &maxNano, &report.MaxSessionTokens)
	if err != nil {
		return report, err
	}
	report.MaxSessionNanoUsd = maxNano
	after := ""
	args := []any{projectID, query.Search}
	if cursor.ID != "" {
		value := "?3"
		if query.Sort == "cost" || query.Sort == "tokens" {
			value = "CAST(?3 AS INTEGER)"
		}
		after = fmt.Sprintf(" AND (%s, id) < (%s, ?4)", expression, value)
		args = append(args, cursor.Value, cursor.ID)
	}
	args = append(args, query.Limit+1)
	// #nosec G202 -- expression is selected from the closed sort vocabulary.
	querySQL := reportSessionTotalsSQL + `, selected AS MATERIALIZED (
 SELECT id, ` + expression + ` AS rank FROM chats WHERE matches` + after + `
 ORDER BY ` + expression + ` DESC, id DESC LIMIT ?` + fmt.Sprint(len(args)) + `)
 SELECT s.id, s.project_id, COALESCE(s.title,''), s.posture, s.status, s.archived_at, s.pin_rank,
 s.created_at, s.activity_at, (SELECT COUNT(*) FROM messages m WHERE m.session_id = s.id), selected.rank
 FROM selected CROSS JOIN sessions s ON s.id = selected.id ORDER BY selected.rank DESC, s.id DESC`
	rows, err := tx.QueryContext(ctx, querySQL, args...)
	if err != nil {
		return report, err
	}
	defer func() { _ = rows.Close() }()
	var last reportCursor
	for rows.Next() {
		var row api.SessionSummary
		var archived sql.NullString
		var pinned sql.NullInt64
		var created, activity, value string
		if err := rows.Scan(&row.ID, &row.ProjectID, &row.Title, &row.Posture, &row.Status, &archived, &pinned, &created, &activity, &row.MessageCount, &value); err != nil {
			return report, err
		}
		if len(report.Sessions) == query.Limit {
			report.NextCursor, err = costReportPages.Encode(scope, last)
			if err != nil {
				return report, err
			}
			break
		}
		if row.CreatedAt, err = db.ParseTime(created); err != nil {
			return report, err
		}
		if row.ActivityAt, err = db.ParseTime(activity); err != nil {
			return report, err
		}
		if row.ArchivedAt, err = db.TimePtrFromNull(archived); err != nil {
			return report, err
		}
		if pinned.Valid {
			rank := int(pinned.Int64)
			row.PinRank = &rank
		}
		report.Sessions = append(report.Sessions, api.ProjectCostSession{Session: row})
		last = reportCursor{Value: value, ID: row.ID}
	}
	return report, rows.Err()
}

func reportSummary(summaries map[string]api.CostSummary, key, projectID string) api.CostSummary {
	summary, ok := summaries[key]
	if !ok {
		summary = buildCostSummary(nil, UnknownCalls{})
	}
	summary.Scope = api.CostScopeProject
	summary.ProjectID = projectID
	return summary
}
