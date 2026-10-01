// Package filebriefing builds and stores source-revision-pinned file briefings.
package filebriefing

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/db"
)

type Status string

const (
	StatusPreview  Status = "preview"
	StatusPending  Status = "pending"
	StatusComplete Status = "complete"
	StatusFailed   Status = "failed"
)

// Preview is available before model generation completes.
type Preview struct {
	Language  string `json:"language,omitempty"`
	LineCount int    `json:"line_count"`
}

// Location is one host-derived declaration the UI may navigate to.
type Location struct {
	Line int    `json:"line"`
	Name string `json:"name"`
	Kind string `json:"kind,omitempty"`
}

type Section struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
}

// Briefing is pinned to one resolved source revision.
type Briefing struct {
	ProjectID, RootID, Path, TargetKey, AttemptID string
	Presentation, SourceSHA256                    string
	Trigger                                       string
	Status                                        Status
	Error                                         string
	FallbackText                                  string
	Truncated                                     bool
	Preview                                       Preview
	Locations                                     []Location
	Sections                                      []Section
	UpdatedAt                                     time.Time
	LastAccessedAt                                time.Time
}

// Outcome belongs to one generation attempt.
type Outcome struct {
	ProjectID, RootID, Path, TargetKey, AttemptID string
	Error                                         string
	FallbackText                                  string
	Sections                                      []Section
	Truncated                                     bool
	UpdatedAt                                     time.Time
}

var ErrNotFound = errors.New("file briefing not found")

type Store interface {
	Get(context.Context, string, string, string, string) (Briefing, error)
	Start(context.Context, Briefing) (Briefing, error)
	Complete(context.Context, Outcome) error
	Preview(context.Context, Outcome) error
	Fail(context.Context, Outcome) error
	Maintain(context.Context, Briefing, RetentionConfig) error
	MaintainDevice(context.Context, RetentionConfig) error
	Clear(context.Context) error
}

// Memory is the process-local Store.
type Memory struct {
	mu   sync.RWMutex
	rows map[string]Briefing
}

func NewMemory() *Memory { return &Memory{rows: map[string]Briefing{}} }
func key(projectID, rootID, path, targetKey string) string {
	return projectID + "\x00" + rootID + "\x00" + path + "\x00" + targetKey
}
func (m *Memory) Get(_ context.Context, projectID, rootID, path, targetKey string) (Briefing, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := key(projectID, rootID, path, targetKey)
	b, ok := m.rows[k]
	if !ok {
		return Briefing{}, ErrNotFound
	}
	b.LastAccessedAt = time.Now().UTC()
	m.rows[k] = b
	return cloneBriefing(b), nil
}
func (m *Memory) Start(_ context.Context, b Briefing) (Briefing, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if b.LastAccessedAt.IsZero() {
		b.LastAccessedAt = b.UpdatedAt
	}
	k := key(b.ProjectID, b.RootID, b.Path, b.TargetKey)
	if prior, ok := m.rows[k]; ok {
		if prior.Status == StatusPending || prior.Status == StatusComplete || (prior.Status == StatusPreview && b.Trigger != "manual") {
			prior.LastAccessedAt = time.Now().UTC()
			m.rows[k] = prior
			return cloneBriefing(prior), nil
		}
	}
	b.AttemptID = uuid.NewString()
	m.rows[k] = cloneBriefing(b)
	return cloneBriefing(b), nil
}
func (m *Memory) Complete(_ context.Context, outcome Outcome) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := key(outcome.ProjectID, outcome.RootID, outcome.Path, outcome.TargetKey)
	b, ok := m.rows[k]
	if !ok || outcome.AttemptID == "" || b.AttemptID != outcome.AttemptID {
		return ErrNotFound
	}
	b.Status, b.Error, b.FallbackText, b.UpdatedAt = StatusComplete, "", "", outcome.UpdatedAt
	b.Sections = cloneSections(outcome.Sections)
	b.Truncated = outcome.Truncated
	m.rows[k] = b
	return nil
}
func (m *Memory) Preview(_ context.Context, outcome Outcome) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := key(outcome.ProjectID, outcome.RootID, outcome.Path, outcome.TargetKey)
	b, ok := m.rows[k]
	if !ok || outcome.AttemptID == "" || b.AttemptID != outcome.AttemptID {
		return ErrNotFound
	}
	b.Status, b.Error, b.FallbackText, b.Truncated, b.UpdatedAt =
		StatusPreview, "", outcome.FallbackText, outcome.Truncated, outcome.UpdatedAt
	m.rows[k] = b
	return nil
}
func (m *Memory) Fail(_ context.Context, outcome Outcome) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := key(outcome.ProjectID, outcome.RootID, outcome.Path, outcome.TargetKey)
	b, ok := m.rows[k]
	if !ok || outcome.AttemptID == "" || b.AttemptID != outcome.AttemptID {
		return ErrNotFound
	}
	b.Status, b.Error, b.FallbackText, b.UpdatedAt = StatusFailed, outcome.Error, "", outcome.UpdatedAt
	m.rows[k] = b
	return nil
}

func (m *Memory) Maintain(_ context.Context, protected Briefing, cfg RetentionConfig) error {
	if err := cfg.validate(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	withinBudget, err := m.pruneRows(func(row Briefing) bool {
		return row.ProjectID == protected.ProjectID && row.RootID == protected.RootID && row.Path == protected.Path
	}, protected, cfg.RevisionsPerFile, 0)
	if err != nil {
		return err
	}
	if !withinBudget {
		return fmt.Errorf("file briefing revision limit cannot be satisfied")
	}
	withinBudget, err = m.pruneRows(
		func(row Briefing) bool { return row.ProjectID == protected.ProjectID },
		protected,
		0,
		cfg.ProjectBytes,
	)
	if err != nil {
		return err
	}
	if !withinBudget {
		return fmt.Errorf("file briefing project budget cannot be satisfied")
	}
	withinBudget, err = m.pruneRows(
		func(Briefing) bool { return true },
		protected,
		cfg.DeviceRows,
		cfg.DeviceBytes,
	)
	if err != nil {
		return err
	}
	if !withinBudget {
		return fmt.Errorf("file briefing device budget cannot be satisfied")
	}
	return nil
}

func (m *Memory) MaintainDevice(_ context.Context, cfg RetentionConfig) error {
	if err := cfg.validate(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	withinBudget, err := m.pruneRows(
		func(Briefing) bool { return true },
		Briefing{},
		cfg.DeviceRows,
		cfg.DeviceBytes,
	)
	if err != nil {
		return err
	}
	if !withinBudget {
		return fmt.Errorf("file briefing device budget cannot be satisfied")
	}
	return nil
}

func (m *Memory) Clear(_ context.Context) error {
	m.mu.Lock()
	clear(m.rows)
	m.mu.Unlock()
	return nil
}

type memoryCandidate struct {
	key          string
	row          Briefing
	storageBytes int64
}

func (m *Memory) pruneRows(
	include func(Briefing) bool,
	protected Briefing,
	maxRows int,
	maxBytes int64,
) (bool, error) {
	rows := make([]memoryCandidate, 0, len(m.rows))
	var usedBytes int64
	for rowKey, row := range m.rows {
		if include(row) {
			storageBytes, err := briefingStorageBytes(row)
			if err != nil {
				return false, err
			}
			rows = append(rows, memoryCandidate{key: rowKey, row: row, storageBytes: storageBytes})
			usedBytes += storageBytes
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if !rows[i].row.LastAccessedAt.Equal(rows[j].row.LastAccessedAt) {
			return rows[i].row.LastAccessedAt.After(rows[j].row.LastAccessedAt)
		}
		return rows[i].key > rows[j].key
	})
	protectedKey := key(protected.ProjectID, protected.RootID, protected.Path, protected.TargetKey)
	dropped := 0
	for i := len(rows) - 1; i >= 0; i-- {
		if (maxRows == 0 || len(rows)-dropped <= maxRows) && (maxBytes == 0 || usedBytes <= maxBytes) {
			break
		}
		if rows[i].key == protectedKey {
			continue
		}
		delete(m.rows, rows[i].key)
		usedBytes -= rows[i].storageBytes
		dropped++
	}
	return (maxRows == 0 || len(rows)-dropped <= maxRows) &&
		(maxBytes == 0 || usedBytes <= maxBytes), nil
}

func briefingStorageBytes(b Briefing) (int64, error) {
	previewJSON, err := json.Marshal(b.Preview)
	if err != nil {
		return 0, fmt.Errorf("marshal file briefing preview: %w", err)
	}
	locationsJSON, err := json.Marshal(b.Locations)
	if err != nil {
		return 0, fmt.Errorf("marshal file briefing locations: %w", err)
	}
	sectionsJSON, err := json.Marshal(b.Sections)
	if err != nil {
		return 0, fmt.Errorf("marshal file briefing sections: %w", err)
	}
	return int64(
		len(b.ProjectID) + len(b.RootID) + len(b.Path) + len(b.TargetKey) + len(b.AttemptID) +
			len(b.Presentation) + len(b.SourceSHA256) + len(b.Trigger) + len(string(b.Status)) +
			len(previewJSON) + len(locationsJSON) + len(sectionsJSON) + len(b.Error) + len(b.FallbackText) +
			len(b.UpdatedAt.UTC().Format(time.RFC3339Nano)) + 144,
	), nil
}

// SQL is the SQLite-backed Store.
type SQL struct{ queries *db.Queries }

func NewSQL(database db.Handle) *SQL { return &SQL{queries: db.New(database)} }
func (s *SQL) Get(ctx context.Context, projectID, rootID, path, targetKey string) (Briefing, error) {
	row, err := s.queries.GetFileBriefing(ctx, db.GetFileBriefingParams{ProjectID: projectID, RootID: rootID, Path: path, TargetKey: targetKey})
	if errors.Is(err, sql.ErrNoRows) {
		return Briefing{}, ErrNotFound
	}
	if err != nil {
		return Briefing{}, err
	}
	accessedAt := time.Now().UTC().Truncate(time.Millisecond)
	if err := s.queries.TouchFileBriefing(ctx, db.TouchFileBriefingParams{
		LastAccessedAtMs: accessedAt.UnixMilli(), ProjectID: projectID,
		RootID: rootID, Path: path, TargetKey: targetKey,
	}); err != nil {
		return Briefing{}, err
	}
	briefing, err := briefingFromRow(row)
	briefing.LastAccessedAt = accessedAt
	return briefing, err
}
func (s *SQL) Start(ctx context.Context, b Briefing) (Briefing, error) {
	previewJSON, err := json.Marshal(b.Preview)
	if err != nil {
		return Briefing{}, err
	}
	locationsJSON, err := json.Marshal(b.Locations)
	if err != nil {
		return Briefing{}, err
	}
	if b.LastAccessedAt.IsZero() {
		b.LastAccessedAt = b.UpdatedAt
	}
	b.AttemptID = uuid.NewString()
	_, err = s.queries.StartFileBriefing(ctx, db.StartFileBriefingParams{
		ProjectID: b.ProjectID, RootID: b.RootID, Path: b.Path, TargetKey: b.TargetKey,
		AttemptID:    b.AttemptID,
		Presentation: b.Presentation, SourceSha256: b.SourceSHA256, Trigger: b.Trigger,
		Status: string(b.Status), PreviewJson: string(previewJSON), LocationsJson: string(locationsJSON),
		UpdatedAt: b.UpdatedAt.UTC().Format(time.RFC3339Nano), LastAccessedAtMs: b.LastAccessedAt.UTC().UnixMilli(),
	})
	if err != nil {
		return Briefing{}, err
	}
	return s.Get(ctx, b.ProjectID, b.RootID, b.Path, b.TargetKey)
}
func (s *SQL) Complete(ctx context.Context, outcome Outcome) error {
	sectionsJSON, err := json.Marshal(cloneSections(outcome.Sections))
	if err != nil {
		return err
	}
	updated, err := s.queries.CompleteFileBriefing(ctx, db.CompleteFileBriefingParams{
		SectionsJson: string(sectionsJSON), Truncated: boolInt64(outcome.Truncated), UpdatedAt: outcome.UpdatedAt.UTC().Format(time.RFC3339Nano),
		ProjectID: outcome.ProjectID, RootID: outcome.RootID, Path: outcome.Path, TargetKey: outcome.TargetKey, AttemptID: outcome.AttemptID,
	})
	if err != nil {
		return err
	}
	if updated == 0 {
		return ErrNotFound
	}
	return nil
}
func (s *SQL) Preview(ctx context.Context, outcome Outcome) error {
	updated, err := s.queries.PreviewFileBriefing(ctx, db.PreviewFileBriefingParams{
		FallbackText: outcome.FallbackText, Truncated: boolInt64(outcome.Truncated),
		UpdatedAt: outcome.UpdatedAt.UTC().Format(time.RFC3339Nano), ProjectID: outcome.ProjectID,
		RootID: outcome.RootID, Path: outcome.Path, TargetKey: outcome.TargetKey, AttemptID: outcome.AttemptID,
	})
	if err != nil {
		return err
	}
	if updated == 0 {
		return ErrNotFound
	}
	return nil
}
func (s *SQL) Fail(ctx context.Context, outcome Outcome) error {
	updated, err := s.queries.FailFileBriefing(ctx, db.FailFileBriefingParams{
		Error: outcome.Error, UpdatedAt: outcome.UpdatedAt.UTC().Format(time.RFC3339Nano),
		ProjectID: outcome.ProjectID, RootID: outcome.RootID, Path: outcome.Path, TargetKey: outcome.TargetKey, AttemptID: outcome.AttemptID,
	})
	if err != nil {
		return err
	}
	if updated == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQL) Maintain(ctx context.Context, protected Briefing, cfg RetentionConfig) error {
	if err := cfg.validate(); err != nil {
		return err
	}
	batch := int64(cfg.MaintenanceBatch)
	if err := s.maintainFile(ctx, protected, cfg.RevisionsPerFile, batch); err != nil {
		return err
	}
	if err := s.maintainProject(ctx, protected, cfg.ProjectBytes, batch); err != nil {
		return err
	}
	return s.maintainDevice(ctx, protected, cfg.DeviceBytes, int64(cfg.DeviceRows), batch)
}

func (s *SQL) MaintainDevice(ctx context.Context, cfg RetentionConfig) error {
	if err := cfg.validate(); err != nil {
		return err
	}
	return s.maintainDevice(
		ctx, Briefing{}, cfg.DeviceBytes, int64(cfg.DeviceRows), int64(cfg.MaintenanceBatch),
	)
}

func (s *SQL) maintainFile(ctx context.Context, protected Briefing, maxRows int, batch int64) error {
	for {
		count, err := s.queries.CountFileBriefings(ctx, db.CountFileBriefingsParams{
			FilterProjectID: protected.ProjectID, FilterRootID: protected.RootID, FilterPath: protected.Path,
		})
		if err != nil {
			return err
		}
		excess := count - int64(maxRows)
		if excess <= 0 {
			return nil
		}
		deleted, err := s.queries.DeleteOldestFileBriefings(ctx, db.DeleteOldestFileBriefingsParams{
			FilterProjectID: protected.ProjectID, FilterRootID: protected.RootID, FilterPath: protected.Path,
			ProtectedTargetKey: protected.TargetKey, DeleteCount: min(excess, batch),
		})
		if err != nil {
			return err
		}
		if deleted == 0 {
			return fmt.Errorf("file briefing revision limit cannot be satisfied")
		}
	}
}

func (s *SQL) maintainProject(ctx context.Context, protected Briefing, maxBytes, batch int64) error {
	for {
		projectBytes, err := s.queries.SumProjectFileBriefingBytes(ctx, protected.ProjectID)
		if err != nil {
			return err
		}
		if projectBytes <= maxBytes {
			return nil
		}
		sizes, err := s.queries.ListOldestProjectFileBriefingSizes(ctx, db.ListOldestProjectFileBriefingSizesParams{
			FilterProjectID: protected.ProjectID, ProtectedRootID: protected.RootID,
			ProtectedPath: protected.Path, ProtectedTargetKey: protected.TargetKey, RowLimit: batch,
		})
		if err != nil {
			return err
		}
		deleteCount := deletionCount(projectBytes, maxBytes, 0, 0, sizes)
		if deleteCount == 0 {
			return fmt.Errorf("file briefing project budget cannot be satisfied")
		}
		deleted, err := s.queries.DeleteOldestProjectFileBriefings(ctx, db.DeleteOldestProjectFileBriefingsParams{
			FilterProjectID: protected.ProjectID, ProtectedRootID: protected.RootID,
			ProtectedPath: protected.Path, ProtectedTargetKey: protected.TargetKey, DeleteCount: deleteCount,
		})
		if err != nil {
			return err
		}
		if deleted == 0 {
			return fmt.Errorf("file briefing project budget cannot be satisfied")
		}
	}
}

func (s *SQL) maintainDevice(ctx context.Context, protected Briefing, maxBytes, maxRows, batch int64) error {
	for {
		deviceBytes, err := s.queries.SumDeviceFileBriefingBytes(ctx)
		if err != nil {
			return err
		}
		deviceRows, err := s.queries.CountDeviceFileBriefings(ctx)
		if err != nil {
			return err
		}
		if deviceBytes <= maxBytes && deviceRows <= maxRows {
			return nil
		}
		sizes, err := s.queries.ListOldestDeviceFileBriefingSizes(ctx, db.ListOldestDeviceFileBriefingSizesParams{
			ProtectedProjectID: protected.ProjectID, ProtectedRootID: protected.RootID,
			ProtectedPath: protected.Path, ProtectedTargetKey: protected.TargetKey, RowLimit: batch,
		})
		if err != nil {
			return err
		}
		deleteCount := deletionCount(deviceBytes, maxBytes, deviceRows, maxRows, sizes)
		if deleteCount == 0 {
			return fmt.Errorf("file briefing device budget cannot be satisfied")
		}
		deleted, err := s.queries.DeleteOldestDeviceFileBriefings(ctx, db.DeleteOldestDeviceFileBriefingsParams{
			ProtectedProjectID: protected.ProjectID, ProtectedRootID: protected.RootID,
			ProtectedPath: protected.Path, ProtectedTargetKey: protected.TargetKey, DeleteCount: deleteCount,
		})
		if err != nil {
			return err
		}
		if deleted == 0 {
			return fmt.Errorf("file briefing device budget cannot be satisfied")
		}
	}
}

func deletionCount(usedBytes, maxBytes, rows, maxRows int64, sizes []sql.NullInt64) int64 {
	rowsToDelete := max(rows-maxRows, 0)
	var count int64
	for _, size := range sizes {
		if usedBytes <= maxBytes && count >= rowsToDelete {
			break
		}
		if size.Valid {
			usedBytes -= size.Int64
		}
		count++
	}
	return count
}

func (s *SQL) Clear(ctx context.Context) error {
	return s.queries.DeleteAllFileBriefings(ctx)
}

func briefingFromRow(row db.FileBriefings) (Briefing, error) {
	updatedAt, err := time.Parse(time.RFC3339Nano, row.UpdatedAt)
	if err != nil {
		return Briefing{}, fmt.Errorf("parse file briefing updated_at: %w", err)
	}
	lastAccessedAt := time.UnixMilli(row.LastAccessedAtMs).UTC()
	var preview Preview
	var locations []Location
	var sections []Section
	if err := json.Unmarshal([]byte(row.PreviewJson), &preview); err != nil {
		return Briefing{}, fmt.Errorf("parse file briefing preview: %w", err)
	}
	if err := json.Unmarshal([]byte(row.LocationsJson), &locations); err != nil {
		return Briefing{}, fmt.Errorf("parse file briefing locations: %w", err)
	}
	if err := json.Unmarshal([]byte(row.SectionsJson), &sections); err != nil {
		return Briefing{}, fmt.Errorf("parse file briefing sections: %w", err)
	}
	return Briefing{
		ProjectID: row.ProjectID, RootID: row.RootID, Path: row.Path, TargetKey: row.TargetKey,
		AttemptID:    row.AttemptID,
		Presentation: row.Presentation, SourceSHA256: row.SourceSha256, Trigger: row.Trigger,
		Status: Status(row.Status), Truncated: row.Truncated != 0, Error: row.Error, FallbackText: row.FallbackText,
		Preview: preview, Locations: locations, Sections: cloneSections(sections),
		UpdatedAt: updatedAt, LastAccessedAt: lastAccessedAt,
	}, nil
}

func cloneBriefing(in Briefing) Briefing {
	in.Locations = append([]Location{}, in.Locations...)
	in.Sections = cloneSections(in.Sections)
	return in
}

func cloneSections(in []Section) []Section {
	out := make([]Section, len(in))
	for i, section := range in {
		out[i] = Section{Kind: section.Kind, Text: section.Text}
	}
	return out
}

func boolInt64(value bool) int64 {
	if value {
		return 1
	}
	return 0
}
