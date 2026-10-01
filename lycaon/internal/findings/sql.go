package findings

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/db"
)

// SQLStore is the durable Store backed by the findings table.
type SQLStore struct {
	queries *db.Queries
}

var _ Store = (*SQLStore)(nil)

// NewSQLStore returns a findings store backed by database.
func NewSQLStore(database db.Handle) *SQLStore {
	return &SQLStore{queries: db.New(database)}
}

func (s *SQLStore) Append(ctx context.Context, sessionID, agent, summary, ref, body string) (bool, error) {
	if s == nil || s.queries == nil {
		return false, fmt.Errorf("findings store not configured")
	}
	key := normalizeKey(sessionID)
	agent, summary, ref = normalizeFindingFields(agent, summary, ref)
	if key == "" || summary == "" {
		return false, fmt.Errorf("finding identity is incomplete")
	}
	inserted, err := s.queries.InsertFinding(ctx, db.InsertFindingParams{
		SessionID: key,
		Agent:     agent,
		Summary:   summary,
		Body:      body,
		Ref:       ref,
		CreatedAt: db.FormatTime(time.Now().UTC()),
	})
	if err != nil {
		return false, err
	}
	return inserted > 0, nil
}

func (s *SQLStore) Recent(ctx context.Context, sessionID, excludeAgent string, afterID int64, limit int, since time.Time) ([]Finding, int64, error) {
	if s == nil || s.queries == nil {
		return nil, afterID, fmt.Errorf("findings store not configured")
	}
	key := normalizeKey(sessionID)

	var scanned []Finding
	if since.IsZero() {
		rows, err := s.queries.ListFindingsAfterID(ctx, db.ListFindingsAfterIDParams{
			SessionID: key, ID: afterID, ExcludeAgent: strings.TrimSpace(excludeAgent),
		})
		if err != nil {
			return nil, afterID, err
		}
		scanned = make([]Finding, 0, len(rows))
		for _, row := range rows {
			scanned = append(scanned, findingFromRow(row.ID, row.Agent, row.Summary, row.Ref, row.Body, row.CreatedAt))
		}
	} else {
		rows, err := s.queries.ListFindingsSinceAfterID(ctx, db.ListFindingsSinceAfterIDParams{
			SessionID:    key,
			CreatedAt:    db.FormatTime(since.UTC()),
			ExcludeAgent: strings.TrimSpace(excludeAgent),
			ID:           afterID,
		})
		if err != nil {
			return nil, afterID, err
		}
		scanned = make([]Finding, 0, len(rows))
		for _, row := range rows {
			scanned = append(scanned, findingFromRow(row.ID, row.Agent, row.Summary, row.Ref, row.Body, row.CreatedAt))
		}
	}

	excludeAgent = strings.TrimSpace(excludeAgent)
	var out []Finding
	for _, f := range scanned {
		if limit > 0 && len(out) >= limit {
			break
		}
		afterID = max(afterID, f.ID)
		if excludeAgent != "" && f.Agent == excludeAgent {
			continue
		}
		out = append(out, f)
	}
	return out, afterID, nil
}

func (s *SQLStore) List(ctx context.Context, sessionID string, max int) ([]Finding, error) {
	if s == nil || s.queries == nil {
		return nil, fmt.Errorf("findings store not configured")
	}
	limit := max
	if limit <= 0 {
		limit = defaultHistoryLimit
	}
	rows, err := s.queries.ListRecentFindings(ctx, db.ListRecentFindingsParams{
		SessionID: normalizeKey(sessionID),
		Limit:     int64(limit),
	})
	if err != nil {
		return nil, err
	}
	// Rows arrive newest-first; callers expect oldest-first.
	out := make([]Finding, len(rows))
	for i, r := range rows {
		out[len(rows)-1-i] = findingFromRow(r.ID, r.Agent, r.Summary, r.Ref, r.Body, r.CreatedAt)
	}
	return out, nil
}

func findingFromRow(id int64, agent, summary, ref, body, createdAt string) Finding {
	ts, err := db.ParseTime(createdAt)
	if err != nil || ts.IsZero() {
		ts, _ = time.Parse(time.RFC3339, createdAt)
	}
	return Finding{ID: id, Agent: agent, Summary: summary, Ref: ref, Body: body, TS: ts}
}

func (s *SQLStore) Get(ctx context.Context, sessionID string, id int64) (Finding, error) {
	row, err := s.queries.GetFinding(ctx, db.GetFindingParams{SessionID: normalizeKey(sessionID), ID: id})
	if db.IsNoRows(err) {
		return Finding{}, ErrNotFound
	}
	if err != nil {
		return Finding{}, err
	}
	return findingFromRow(row.ID, row.Agent, row.Summary, row.Ref, row.Body, row.CreatedAt), nil
}

func (s *SQLStore) Delivery(ctx context.Context, job string) (Delivery, error) {
	row, err := s.queries.LatestWorkerFindingDelivery(ctx, job)
	if db.IsNoRows(err) {
		return Delivery{}, nil
	}
	if err != nil {
		return Delivery{}, err
	}
	d := Delivery{Cursor: row.Cursor}
	err = json.Unmarshal([]byte(row.NotesJson), &d.Notes)
	return d, err
}
func (s *SQLStore) CommitDelivery(ctx context.Context, job, response string, d Delivery) error {
	raw, err := json.Marshal(d.Notes)
	if err != nil {
		return err
	}
	return s.queries.RecordWorkerFindingDelivery(ctx, db.RecordWorkerFindingDeliveryParams{WorkerJobID: job, ResponseID: response, Cursor: d.Cursor, NotesJson: string(raw)})
}
