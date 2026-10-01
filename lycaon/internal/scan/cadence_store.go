package scan

import (
	"context"
	"encoding/json"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/pkg/api"
)

// SeriesRow retains pending changes alongside one active scanner generation.
type SeriesRow struct {
	CanonicalPath                   string
	ScannerID                       string
	Categories                      []api.ScanCategory
	DesiredPassID                   string
	DesiredPaths                    []string
	DesiredTrigger                  api.ScanTrigger
	DirtySinceAt                    time.Time
	DueAt                           time.Time
	MaxDueAt                        time.Time
	DispatchToken                   string
	ClaimHeartbeatAt                time.Time
	DispatchPassID                  string
	DispatchPaths                   []string
	DispatchTrigger                 api.ScanTrigger
	ActiveScanID                    string
	LastFileCount                   int
	LastStartedAt                   time.Time
	LastCompletedAt                 time.Time
	LastSuccessfulScanID            string
	LastCoveredSnapshotID           string
	LastCoveredExecutionFingerprint string
	UpdatedAt                       time.Time
}

func (row SeriesRow) WantsDispatch() bool {
	return row.DesiredPassID != "" || len(row.DesiredPaths) > 0 || row.DesiredTrigger != ""
}

// OwesPass reports whether the series still owes passID its member scan.
func (row SeriesRow) OwesPass(passID string) bool {
	return passID != "" && (row.DesiredPassID == passID || row.DispatchPassID == passID)
}

func seriesFromRow(row db.ScanSeries) SeriesRow {
	return SeriesRow{
		CanonicalPath: row.CanonicalPath, ScannerID: row.ScannerID,
		Categories:     parseSeriesCategories(row.CategoriesJson),
		DesiredPassID:  row.DesiredPassID,
		DesiredPaths:   parseSeriesPaths(row.DesiredPathsJson),
		DesiredTrigger: api.ScanTrigger(row.DesiredTrigger),
		DirtySinceAt:   parseCadenceTime(row.DirtySinceAt), DueAt: parseCadenceTime(row.DueAt),
		MaxDueAt: parseCadenceTime(row.MaxDueAt), DispatchToken: row.DispatchToken,
		ClaimHeartbeatAt: parseCadenceTime(row.ClaimHeartbeatAt),
		DispatchPassID:   row.DispatchPassID,
		DispatchPaths:    parseSeriesPaths(row.DispatchPathsJson),
		DispatchTrigger:  api.ScanTrigger(row.DispatchTrigger), ActiveScanID: row.ActiveScanID,
		LastFileCount: int(row.LastFileCount), LastStartedAt: parseCadenceTime(row.LastStartedAt),
		LastCompletedAt:                 parseCadenceTime(row.LastCompletedAt),
		LastSuccessfulScanID:            row.LastSuccessfulScanID,
		LastCoveredSnapshotID:           row.LastCoveredSnapshotID,
		LastCoveredExecutionFingerprint: row.LastCoveredExecutionFingerprint,
		UpdatedAt:                       parseCadenceTime(row.UpdatedAt),
	}
}

func parseSeriesCategories(raw string) []api.ScanCategory {
	var categories []api.ScanCategory
	_ = json.Unmarshal([]byte(raw), &categories)
	return categories
}

func parseSeriesPaths(raw string) []string {
	var paths []string
	_ = json.Unmarshal([]byte(raw), &paths)
	return paths
}

func parseCadenceTime(raw string) time.Time {
	t, err := db.ParseTime(raw)
	if err != nil {
		return time.Time{}
	}
	return t
}

func formatCadenceTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return db.FormatTime(t.UTC())
}

func seriesPathsJSON(paths []string) string {
	if len(paths) == 0 {
		return "[]"
	}
	b, err := surveyjson.Marshal(paths)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func (s *SQLStore) GetSeries(ctx context.Context, canonicalPath, scannerID string) (*SeriesRow, error) {
	row, err := s.queries.GetScanSeries(ctx, db.GetScanSeriesParams{
		CanonicalPath: canonicalPath, ScannerID: scannerID,
	})
	if db.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := seriesFromRow(row)
	return &out, nil
}

func (s *SQLStore) ListSeriesForPath(ctx context.Context, canonicalPath string) ([]SeriesRow, error) {
	rows, err := s.queries.ListScanSeriesForPath(ctx, canonicalPath)
	if err != nil {
		return nil, err
	}
	out := make([]SeriesRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, seriesFromRow(row))
	}
	return out, nil
}

func (s *SQLStore) ListDueSeries(ctx context.Context, now time.Time) ([]SeriesRow, error) {
	rows, err := s.queries.ListDueScanSeries(ctx, db.ListDueScanSeriesParams{
		NowAt: formatCadenceTime(now), RecoverBeforeAt: formatCadenceTime(now.Add(-DispatchClaimTTL)),
	})
	if err != nil {
		return nil, err
	}
	out := make([]SeriesRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, seriesFromRow(row))
	}
	return out, nil
}

func (s *SQLStore) ListActiveSeries(ctx context.Context) ([]SeriesRow, error) {
	rows, err := s.queries.ListActiveScanSeries(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]SeriesRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, seriesFromRow(row))
	}
	return out, nil
}

func (s *SQLStore) SeriesByActiveScan(ctx context.Context, scanID string) (*SeriesRow, error) {
	row, err := s.queries.GetScanSeriesByActiveScan(ctx, scanID)
	if db.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := seriesFromRow(row)
	return &out, nil
}

// HeartbeatClaim extends the claim only while its token still matches.
func (s *SQLStore) HeartbeatClaim(ctx context.Context, row SeriesRow, at time.Time) error {
	if row.DispatchToken == "" {
		return nil
	}
	_, err := s.queries.HeartbeatScanSeriesClaim(ctx, db.HeartbeatScanSeriesClaimParams{
		HeartbeatAt: formatCadenceTime(at), CanonicalPath: row.CanonicalPath,
		ScannerID: row.ScannerID, DispatchToken: row.DispatchToken,
	})
	return err
}

func (s *SQLStore) DeleteSeries(ctx context.Context, canonicalPath, scannerID string) error {
	return s.queries.DeleteScanSeries(ctx, db.DeleteScanSeriesParams{
		CanonicalPath: canonicalPath, ScannerID: scannerID,
	})
}

func (s *SQLStore) UpsertSeries(ctx context.Context, row SeriesRow) error {
	categories, err := categoriesJSON(row.Categories)
	if err != nil {
		return err
	}
	err = s.queries.UpsertScanSeries(ctx, db.UpsertScanSeriesParams{
		CanonicalPath: row.CanonicalPath, ScannerID: row.ScannerID, CategoriesJson: categories,
		DesiredPassID:    row.DesiredPassID,
		DesiredPathsJson: seriesPathsJSON(row.DesiredPaths), DesiredTrigger: string(row.DesiredTrigger),
		DirtySinceAt: formatCadenceTime(row.DirtySinceAt), DueAt: formatCadenceTime(row.DueAt),
		MaxDueAt: formatCadenceTime(row.MaxDueAt), DispatchToken: row.DispatchToken,
		ClaimHeartbeatAt: formatCadenceTime(row.ClaimHeartbeatAt),
		DispatchPassID:   row.DispatchPassID, DispatchPathsJson: seriesPathsJSON(row.DispatchPaths),
		DispatchTrigger: string(row.DispatchTrigger), ActiveScanID: row.ActiveScanID,
		LastFileCount: int64(row.LastFileCount), LastStartedAt: formatCadenceTime(row.LastStartedAt),
		LastCompletedAt: formatCadenceTime(row.LastCompletedAt), LastSuccessfulScanID: row.LastSuccessfulScanID,
		LastCoveredSnapshotID:           row.LastCoveredSnapshotID,
		LastCoveredExecutionFingerprint: row.LastCoveredExecutionFingerprint, UpdatedAt: formatCadenceTime(row.UpdatedAt),
	})
	if err == nil {
		s.SeriesChanged.Notify()
	}
	return err
}

// DispatchClaimTTL bounds a persisted dispatch lease without a heartbeat.
const DispatchClaimTTL = 2 * time.Minute

func (s *SQLStore) NextSeriesDue(ctx context.Context) (time.Time, error) {
	raw, err := s.queries.NextScanSeriesDue(ctx)
	return parseCadenceTime(raw), err
}
func (s *SQLStore) OldestDispatchHeartbeat(ctx context.Context) (time.Time, error) {
	raw, err := s.queries.OldestScanDispatchHeartbeat(ctx)
	return parseCadenceTime(raw), err
}
func (s *SQLStore) FindingChangesSince(ctx context.Context, canonical string, since time.Time) (int, int, error) {
	counts, err := s.queries.CountScanFindingEventsSince(ctx, db.CountScanFindingEventsSinceParams{CanonicalPath: canonical, ObservedAt: db.FormatTime(since)})
	if err != nil {
		return 0, 0, err
	}
	return int(asInt64(counts.Introduced)), int(asInt64(counts.Fixed)), nil
}

func asInt64(value any) int64 {
	switch v := value.(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case float64:
		return int64(v)
	}
	return 0
}
