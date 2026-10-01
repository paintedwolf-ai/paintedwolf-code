package webindex

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// maxOutcomeRows caps the search-outcome log by recency.
const maxOutcomeRows = 200

// QueueSearchOutcome appends one live direct search's result shape
// asynchronously: the query and how many content-bearing hits it ended with.
// The starved-query re-warm consumes these.
func (s *Store) QueueSearchOutcome(ctx context.Context, query, projectID, projectDir string, strongHits, maxResults int) {
	if s == nil {
		return
	}
	query = strings.TrimSpace(query)
	projectID = strings.TrimSpace(projectID)
	projectDir = strings.TrimSpace(projectDir)
	if query == "" {
		return
	}
	at := time.Now().Unix()
	ctx = context.WithoutCancel(ctx)
	s.enqueue(func(db *sql.DB) error {
		if _, err := db.ExecContext(ctx, `INSERT INTO search_outcomes (at, query, project_id, project_dir, strong_hits, max_results)
			VALUES (?, ?, ?, ?, ?, ?)`, at, query, projectID, projectDir, strongHits, maxResults); err != nil {
			return fmt.Errorf("record search outcome: %w", err)
		}
		if _, err := db.ExecContext(ctx, `DELETE FROM search_outcomes WHERE id NOT IN (
			SELECT id FROM search_outcomes ORDER BY at DESC, id DESC LIMIT ?)`, maxOutcomeRows); err != nil {
			return fmt.Errorf("cap search outcome rows: %w", err)
		}
		return nil
	})
}

// StarvedQueries returns queries whose most recent live search ended starved —
// content-bearing hits under half the ask — and that no re-warm has spent a
// seed call on yet, newest first. A later successful search for the same
// query removes it from the starved set by superseding the older outcome.
type StarvedQuery struct {
	Query      string
	ProjectID  string
	ProjectDir string
}

func (s *Store) StarvedQueries(ctx context.Context, n int) ([]StarvedQuery, error) {
	if s == nil || n <= 0 {
		return nil, nil
	}
	rows, err := s.readDB.QueryContext(ctx, `SELECT query, project_id, project_dir FROM search_outcomes so
		WHERE id = (SELECT id FROM search_outcomes WHERE query = so.query AND project_id = so.project_id ORDER BY at DESC, id DESC LIMIT 1)
		AND rewarmed = 0 AND strong_hits * 2 < max_results
		ORDER BY at DESC LIMIT ?`, n)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []StarvedQuery
	for rows.Next() {
		var q StarvedQuery
		if err := rows.Scan(&q.Query, &q.ProjectID, &q.ProjectDir); err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

// MarkQueryRewarmed flags every outcome row for query as re-warmed so one
// starved query never earns a second scheduled seed call.
func (s *Store) MarkQueryRewarmed(ctx context.Context, query, projectID string) {
	if s == nil || strings.TrimSpace(query) == "" {
		return
	}
	ctx = context.WithoutCancel(ctx)
	s.enqueue(func(db *sql.DB) error {
		if _, err := db.ExecContext(ctx,
			`UPDATE search_outcomes SET rewarmed = 1 WHERE query = ? AND project_id = ?`,
			query, strings.TrimSpace(projectID)); err != nil {
			return fmt.Errorf("mark outcome rewarmed: %w", err)
		}
		return nil
	})
}
