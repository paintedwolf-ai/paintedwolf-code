package workflow

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

// SessionWorkflowRecord is one session-scoped manifest row.
type SessionWorkflowRecord struct {
	SessionID            string
	WorkflowID           string
	Version              string
	ManifestYAML         string
	EffectiveSummaryJSON string
	EffectiveSummary     api.ComposeEffectiveSummary
	CreatedAt            time.Time
	CreatedBy            ComposeActor
	// CreatedByPersonID names the person when CreatedBy is the user.
	CreatedByPersonID string
}

// composingPerson names the person behind a user-composed manifest; the
// coordinator composes as nobody.
func composingPerson(ctx context.Context, createdBy ComposeActor) (string, error) {
	if createdBy != ComposeActorUser {
		return "", nil
	}
	person, err := people.Deciding(ctx)
	if err != nil {
		return "", fmt.Errorf("session workflow composer: %w", err)
	}
	return person.ID, nil
}

// SessionWorkflowStore persists ephemeral session-tier workflow manifests.
type SessionWorkflowStore interface {
	Upsert(ctx context.Context, sessionID string, manifestYAML []byte, createdBy ComposeActor, summary *api.ComposeEffectiveSummary) error
	Delete(ctx context.Context, sessionID, workflowID, version string) error
	ListBySession(ctx context.Context, sessionID string) ([]SessionWorkflowRecord, error)
	Get(ctx context.Context, sessionID, workflowID, version string) (*SessionWorkflowRecord, error)
}

// SessionWorkflowSQLStore implements SessionWorkflowStore in SQLite.
type SessionWorkflowSQLStore struct {
	queries *db.Queries
}

// NewSessionWorkflowSQLStore creates a session workflow store.
func NewSessionWorkflowSQLStore(database db.Handle) *SessionWorkflowSQLStore {
	return &SessionWorkflowSQLStore{queries: db.New(database)}
}

// Upsert inserts or replaces a session manifest (minimum schema validation at parse).
func (s *SessionWorkflowSQLStore) Upsert(ctx context.Context, sessionID string, manifestYAML []byte, createdBy ComposeActor, summary *api.ComposeEffectiveSummary) error {
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
	if !IsComposeCreatedBy(createdBy) {
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
func (s *SessionWorkflowSQLStore) Delete(ctx context.Context, sessionID, workflowID, version string) error {
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
		return ErrSessionWorkflowNotFound
	}
	return nil
}

// ListBySession returns session manifests ordered by workflow id then version descending.
func (s *SessionWorkflowSQLStore) ListBySession(ctx context.Context, sessionID string) ([]SessionWorkflowRecord, error) {
	if s == nil || s.queries == nil {
		return nil, fmt.Errorf("session workflow store not configured")
	}
	rows, err := s.queries.ListSessionWorkflows(ctx, strings.TrimSpace(sessionID))
	if err != nil {
		return nil, err
	}
	out := make([]SessionWorkflowRecord, 0, len(rows))
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
func (s *SessionWorkflowSQLStore) Get(ctx context.Context, sessionID, workflowID, version string) (*SessionWorkflowRecord, error) {
	if s == nil || s.queries == nil {
		return nil, fmt.Errorf("session workflow store not configured")
	}
	row, err := s.queries.GetSessionWorkflow(ctx, db.GetSessionWorkflowParams{
		SessionID:  strings.TrimSpace(sessionID),
		WorkflowID: strings.TrimSpace(workflowID),
		Version:    strings.TrimSpace(version),
	})
	if db.IsNoRows(err) {
		return nil, ErrSessionWorkflowNotFound
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
func LatestEffectiveSummary(records []SessionWorkflowRecord) (api.ComposeEffectiveSummary, bool) {
	if len(records) == 0 {
		return api.ComposeEffectiveSummary{}, false
	}
	sorted := append([]SessionWorkflowRecord(nil), records...)
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
func EffectiveSummaryByKey(records []SessionWorkflowRecord, key string) (api.ComposeEffectiveSummary, bool) {
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
func sessionWorkflowFromRow(r db.SessionWorkflows) (SessionWorkflowRecord, error) {
	rec := SessionWorkflowRecord{
		SessionID:            r.SessionID,
		WorkflowID:           r.WorkflowID,
		Version:              r.Version,
		ManifestYAML:         r.ManifestYaml,
		EffectiveSummaryJSON: r.EffectiveSummaryJson,
		CreatedBy:            ComposeActor(r.CreatedBy),
		CreatedByPersonID:    db.StringFromNull(r.CreatedByPersonID),
	}
	t, err := db.ParseTime(r.CreatedAt)
	if err != nil {
		return SessionWorkflowRecord{}, err
	}
	rec.CreatedAt = t
	summary, err := parseEffectiveSummaryJSON(rec.EffectiveSummaryJSON)
	if err != nil {
		return SessionWorkflowRecord{}, err
	}
	rec.EffectiveSummary = summary
	return rec, nil
}

// MemorySessionWorkflowStore is an in-memory SessionWorkflowStore for tests.
type MemorySessionWorkflowStore struct {
	rows map[string][]SessionWorkflowRecord
}

// NewMemorySessionWorkflowStore creates an empty memory store.
func NewMemorySessionWorkflowStore() *MemorySessionWorkflowStore {
	return &MemorySessionWorkflowStore{rows: map[string][]SessionWorkflowRecord{}}
}

// Upsert stores or replaces a session manifest.
func (s *MemorySessionWorkflowStore) Upsert(ctx context.Context, sessionID string, manifestYAML []byte, createdBy ComposeActor, summary *api.ComposeEffectiveSummary) error {
	if s == nil {
		return fmt.Errorf("session workflow store not configured")
	}
	m, err := workflowdef.ParseManifestYAML(manifestYAML)
	if err != nil {
		return err
	}
	if !IsComposeCreatedBy(createdBy) {
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
	rec := SessionWorkflowRecord{
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
func (s *MemorySessionWorkflowStore) Delete(ctx context.Context, sessionID, workflowID, version string) error {
	_ = ctx
	if s == nil {
		return fmt.Errorf("session workflow store not configured")
	}
	key := workflowdef.ManifestKey(workflowID, version)
	list := s.rows[sessionID]
	var next []SessionWorkflowRecord
	found := false
	for _, rec := range list {
		if workflowdef.ManifestKey(rec.WorkflowID, rec.Version) == key {
			found = true
			continue
		}
		next = append(next, rec)
	}
	if !found {
		return ErrSessionWorkflowNotFound
	}
	s.rows[sessionID] = next
	return nil
}

// ListBySession returns manifests for a session.
func (s *MemorySessionWorkflowStore) ListBySession(ctx context.Context, sessionID string) ([]SessionWorkflowRecord, error) {
	_ = ctx
	if s == nil {
		return nil, fmt.Errorf("session workflow store not configured")
	}
	return append([]SessionWorkflowRecord(nil), s.rows[sessionID]...), nil
}

// Get returns one session manifest.
func (s *MemorySessionWorkflowStore) Get(ctx context.Context, sessionID, workflowID, version string) (*SessionWorkflowRecord, error) {
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
	return nil, ErrSessionWorkflowNotFound
}
