package drafts

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/people"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

// Record is one session-scoped manifest row.
type Record struct {
	SessionID            string
	WorkflowID           string
	Version              string
	ManifestYAML         string
	EffectiveSummaryJSON string
	EffectiveSummary     api.ComposeEffectiveSummary
	CreatedAt            time.Time
	CreatedBy            Actor
	// CreatedByPersonID names the person when CreatedBy is the user.
	CreatedByPersonID string
}

// composingPerson names the person behind a user-composed manifest; the
// coordinator composes as nobody.
func composingPerson(ctx context.Context, createdBy Actor) (string, error) {
	if createdBy != User {
		return "", nil
	}
	person, err := people.Deciding(ctx)
	if err != nil {
		return "", fmt.Errorf("session workflow composer: %w", err)
	}
	return person.ID, nil
}

// Store persists ephemeral session-tier workflow manifests.
type Store interface {
	Upsert(ctx context.Context, sessionID string, manifestYAML []byte, createdBy Actor, summary *api.ComposeEffectiveSummary) error
	Delete(ctx context.Context, sessionID, workflowID, version string) error
	ListBySession(ctx context.Context, sessionID string) ([]Record, error)
	Get(ctx context.Context, sessionID, workflowID, version string) (*Record, error)
}

// SQL implements Store in SQLite.
type SQL struct {
	queries *db.Queries
}

// NewSQL creates a session workflow store.
func NewSQL(database db.Handle) *SQL {
	return &SQL{queries: db.New(database)}
}

// Upsert inserts or replaces a session manifest (minimum schema validation at parse).
func (s *SQL) Upsert(ctx context.Context, sessionID string, manifestYAML []byte, createdBy Actor, summary *api.ComposeEffectiveSummary) error {
	if s == nil || s.queries == nil {
		return fmt.Errorf("session workflow store not configured")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return fmt.Errorf("session_id required")
	}
	m, err := workflowdef.ParseManifestYAML(manifestYAML)
	if err != nil {
		return err
	}
	if !IsActor(createdBy) {
		return fmt.Errorf("invalid compose actor %q", createdBy)
	}
	personID, err := composingPerson(ctx, createdBy)
	if err != nil {
		return err
	}
	summaryJSON, err := marshalEffectiveSummary(summary)
	if err != nil {
		return err
	}
	return s.queries.UpsertSessionWorkflow(ctx, db.UpsertSessionWorkflowParams{
		SessionID:            sessionID,
		WorkflowID:           m.ID,
		Version:              m.Version,
		ManifestYaml:         string(manifestYAML),
		EffectiveSummaryJson: summaryJSON,
		CreatedAt:            db.FormatTime(time.Now().UTC()),
		CreatedBy:            string(createdBy),
		CreatedByPersonID:    db.NullString(personID),
	})
}

// Delete removes one session manifest row.
func (s *SQL) Delete(ctx context.Context, sessionID, workflowID, version string) error {
	if s == nil || s.queries == nil {
		return fmt.Errorf("session workflow store not configured")
	}
	n, err := s.queries.DeleteSessionWorkflow(ctx, db.DeleteSessionWorkflowParams{
		SessionID:  strings.TrimSpace(sessionID),
		WorkflowID: strings.TrimSpace(workflowID),
		Version:    strings.TrimSpace(version),
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListBySession returns session manifests ordered by workflow id then version descending.
func (s *SQL) ListBySession(ctx context.Context, sessionID string) ([]Record, error) {
	if s == nil || s.queries == nil {
		return nil, fmt.Errorf("session workflow store not configured")
	}
	rows, err := s.queries.ListSessionWorkflows(ctx, strings.TrimSpace(sessionID))
	if err != nil {
		return nil, err
	}
	out := make([]Record, 0, len(rows))
	for _, r := range rows {
		rec, err := sessionWorkflowFromRow(r)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, nil
}

// Get returns one session manifest row.
func (s *SQL) Get(ctx context.Context, sessionID, workflowID, version string) (*Record, error) {
	if s == nil || s.queries == nil {
		return nil, fmt.Errorf("session workflow store not configured")
	}
	row, err := s.queries.GetSessionWorkflow(ctx, db.GetSessionWorkflowParams{
		SessionID:  strings.TrimSpace(sessionID),
		WorkflowID: strings.TrimSpace(workflowID),
		Version:    strings.TrimSpace(version),
	})
	if db.IsNoRows(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	rec, err := sessionWorkflowFromRow(row)
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

// LatestEffectiveSummary returns the most recently upserted compose summary for a session.
func LatestEffectiveSummary(records []Record) (api.ComposeEffectiveSummary, bool) {
	if len(records) == 0 {
		return api.ComposeEffectiveSummary{}, false
	}
	sorted := append([]Record(nil), records...)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].CreatedAt.After(sorted[j].CreatedAt)
	})
	for _, rec := range sorted {
		if strings.TrimSpace(rec.EffectiveSummary.CoordinatorBrief) != "" || len(rec.EffectiveSummary.Phases) > 0 {
			return rec.EffectiveSummary, true
		}
	}
	return sorted[0].EffectiveSummary, true
}

// EffectiveSummaryByKey loads summary for workflow_id@version.
func EffectiveSummaryByKey(records []Record, key string) (api.ComposeEffectiveSummary, bool) {
	key = strings.ToLower(strings.TrimSpace(key))
	for _, rec := range records {
		if workflowdef.ManifestKey(rec.WorkflowID, rec.Version) == key {
			return rec.EffectiveSummary, true
		}
	}
	return api.ComposeEffectiveSummary{}, false
}

func marshalEffectiveSummary(summary *api.ComposeEffectiveSummary) (string, error) {
	if summary == nil {
		return "{}", nil
	}
	raw, err := json.Marshal(summary)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func parseEffectiveSummaryJSON(raw string) (api.ComposeEffectiveSummary, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" {
		return api.ComposeEffectiveSummary{}, nil
	}
	var summary api.ComposeEffectiveSummary
	if err := json.Unmarshal([]byte(raw), &summary); err != nil {
		return api.ComposeEffectiveSummary{}, err
	}
	return summary, nil
}

// sessionWorkflowFromRow maps a generated session_workflows row onto the record type.
func sessionWorkflowFromRow(r db.SessionWorkflows) (Record, error) {
	rec := Record{
		SessionID:            r.SessionID,
		WorkflowID:           r.WorkflowID,
		Version:              r.Version,
		ManifestYAML:         r.ManifestYaml,
		EffectiveSummaryJSON: r.EffectiveSummaryJson,
		CreatedBy:            Actor(r.CreatedBy),
		CreatedByPersonID:    db.StringFromNull(r.CreatedByPersonID),
	}
	t, err := db.ParseTime(r.CreatedAt)
	if err != nil {
		return Record{}, err
	}
	rec.CreatedAt = t
	summary, err := parseEffectiveSummaryJSON(rec.EffectiveSummaryJSON)
	if err != nil {
		return Record{}, err
	}
	rec.EffectiveSummary = summary
	return rec, nil
}

// Memory is an in-memory Store for tests.
type Memory struct {
	rows map[string][]Record
}

// NewMemory creates an empty memory store.
func NewMemory() *Memory {
	return &Memory{rows: map[string][]Record{}}
}

// Upsert stores or replaces a session manifest.
func (s *Memory) Upsert(ctx context.Context, sessionID string, manifestYAML []byte, createdBy Actor, summary *api.ComposeEffectiveSummary) error {
	if s == nil {
		return fmt.Errorf("session workflow store not configured")
	}
	m, err := workflowdef.ParseManifestYAML(manifestYAML)
	if err != nil {
		return err
	}
	if !IsActor(createdBy) {
		return fmt.Errorf("invalid compose actor %q", createdBy)
	}
	personID, err := composingPerson(ctx, createdBy)
	if err != nil {
		return err
	}
	summaryJSON, err := marshalEffectiveSummary(summary)
	if err != nil {
		return err
	}
	parsed, err := parseEffectiveSummaryJSON(summaryJSON)
	if err != nil {
		return err
	}
	key := workflowdef.ManifestKey(m.ID, m.Version)
	rec := Record{
		SessionID:            sessionID,
		WorkflowID:           m.ID,
		Version:              m.Version,
		ManifestYAML:         string(manifestYAML),
		EffectiveSummaryJSON: summaryJSON,
		EffectiveSummary:     parsed,
		CreatedAt:            time.Now().UTC(),
		CreatedBy:            createdBy,
		CreatedByPersonID:    personID,
	}
	list := s.rows[sessionID]
	found := false
	for i, existing := range list {
		if workflowdef.ManifestKey(existing.WorkflowID, existing.Version) == key {
			list[i] = rec
			found = true
			break
		}
	}
	if !found {
		list = append(list, rec)
	}
	s.rows[sessionID] = list
	return nil
}

// Delete removes one session manifest.
func (s *Memory) Delete(ctx context.Context, sessionID, workflowID, version string) error {
	_ = ctx
	if s == nil {
		return fmt.Errorf("session workflow store not configured")
	}
	key := workflowdef.ManifestKey(workflowID, version)
	list := s.rows[sessionID]
	var next []Record
	found := false
	for _, rec := range list {
		if workflowdef.ManifestKey(rec.WorkflowID, rec.Version) == key {
			found = true
			continue
		}
		next = append(next, rec)
	}
	if !found {
		return ErrNotFound
	}
	s.rows[sessionID] = next
	return nil
}

// ListBySession returns manifests for a session.
func (s *Memory) ListBySession(ctx context.Context, sessionID string) ([]Record, error) {
	_ = ctx
	if s == nil {
		return nil, fmt.Errorf("session workflow store not configured")
	}
	return append([]Record(nil), s.rows[sessionID]...), nil
}

// Get returns one session manifest.
func (s *Memory) Get(ctx context.Context, sessionID, workflowID, version string) (*Record, error) {
	_ = ctx
	if s == nil {
		return nil, fmt.Errorf("session workflow store not configured")
	}
	key := workflowdef.ManifestKey(workflowID, version)
	for _, rec := range s.rows[sessionID] {
		if workflowdef.ManifestKey(rec.WorkflowID, rec.Version) == key {
			copy := rec
			return &copy, nil
		}
	}
	return nil, ErrNotFound
}
