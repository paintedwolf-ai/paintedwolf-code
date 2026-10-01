package scan

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/pagecursor"
	scanignore "github.com/lycaon/lycaon/internal/scan/ignores"
	"github.com/lycaon/lycaon/pkg/api"
)

const (
	ledgerDefaultLimit = 50
	MaxLedgerPageSize  = 500
)

var findingLedgerPages = pagecursor.For[int]("finding_ledger")

func (s *SQLStore) FindingLedger(ctx context.Context, canonicalPath string, req api.FindingLedgerQueryRequest) (*api.FindingLedgerResponse, error) {
	if s == nil {
		return nil, fmt.Errorf("scan store not configured")
	}
	filter, err := newLedgerFilter(canonicalPath, req)
	if err != nil {
		return nil, err
	}
	order := ledgerSortOrder(req.Sort, req.Order)
	descending := "0"
	if order.descending {
		descending = "1"
	}
	scope := pagecursor.Scope(canonicalPath, filter.FilterJson, string(order.key), descending)
	offset := 0
	if req.Cursor != "" {
		dec, err := findingLedgerPages.Decode(req.Cursor, scope)
		if err != nil {
			return nil, err
		}
		offset = dec
	}
	offset = max(offset, 0)
	limit := req.Limit
	if limit <= 0 {
		limit = ledgerDefaultLimit
	}
	limit = min(limit, MaxLedgerPageSize)

	total, err := s.countLedger(ctx, filter)
	if err != nil {
		return nil, err
	}
	entries, err := s.ledgerPage(ctx, filter, order, limit, offset)
	if err != nil {
		return nil, err
	}
	counts, byLevel, err := s.ledgerTotals(ctx, canonicalPath)
	if err != nil {
		return nil, err
	}
	out := &api.FindingLedgerResponse{
		Entries: entries, Counts: counts, ByLevel: byLevel,
		TotalMatch: total,
	}
	if offset+len(entries) < total {
		nextCursor, err := findingLedgerPages.Encode(scope, offset+len(entries))
		if err != nil {
			return nil, err
		}
		out.NextCursor = nextCursor
	}
	return out, nil
}

func (s *SQLStore) countLedger(ctx context.Context, filter db.CountFindingLedgerParams) (int, error) {
	total, err := s.queries.CountFindingLedger(ctx, filter)
	if err != nil {
		return 0, fmt.Errorf("count finding ledger: %w", err)
	}
	return int(total), nil
}

func (s *SQLStore) ledgerPage(ctx context.Context, filter db.CountFindingLedgerParams, order ledgerOrder, limit, offset int) ([]api.FindingLedgerEntry, error) {
	descending := int64(0)
	if order.descending {
		descending = 1
	}
	rows, err := s.queries.ListFindingLedger(ctx, db.ListFindingLedgerParams{
		CanonicalPath: filter.CanonicalPath, FilterJson: filter.FilterJson,
		SortKey: string(order.key), SortDescending: descending,
		PageLimit: int64(limit), PageOffset: int64(offset),
	})
	if err != nil {
		return nil, fmt.Errorf("read finding ledger: %w", err)
	}
	out := make([]api.FindingLedgerEntry, 0, len(rows))
	for _, row := range rows {
		entry, err := ledgerEntryFrom(row)
		if err != nil {
			return nil, err
		}
		out = append(out, entry)
	}
	return out, nil
}

func ledgerEntryFrom(row db.ListFindingLedgerRow) (api.FindingLedgerEntry, error) {
	var finding api.SecurityFinding
	if err := json.Unmarshal([]byte(row.FindingJson), &finding); err != nil {
		return api.FindingLedgerEntry{}, fmt.Errorf("decode ledger finding %s: %w", row.Fingerprint, err)
	}
	firstSeen, err := db.ParseTime(row.FirstSeenAt)
	if err != nil {
		return api.FindingLedgerEntry{}, fmt.Errorf("parse first_seen_at for %s: %w", row.Fingerprint, err)
	}
	finding.History = &api.SecurityFindingHistory{
		IntroducedAt:         firstSeen,
		IntroducedSnapshotID: row.FirstSnapshotID,
		IntroducedScanID:     row.FirstScanID,
	}
	entry := api.FindingLedgerEntry{
		Finding:      finding,
		State:        api.FindingLedgerState(row.State),
		ScannerID:    row.ScannerID,
		FirstSeenAt:  firstSeen,
		Observations: int(row.Observations),
		LastScanID:   row.LastScanID,
	}
	lastEvent, err := db.ParseTime(row.LastEventAt)
	if err != nil {
		return api.FindingLedgerEntry{}, fmt.Errorf("parse last_event_at for %s: %w", row.Fingerprint, err)
	}
	entry.LastSeenAt = ledgerLastSeen(entry.State, lastEvent, row.SeriesCompletedAt)
	if entry.State != api.FindingLedgerOpen && entry.State != api.FindingLedgerReopened {
		entry.Absence = &api.FindingAbsence{
			ObservedAt:     lastEvent,
			ScanID:         row.LastScanID,
			CoverageStatus: api.ScanCoverageStatus(row.LeftCoverage),
			ExecutionMoved: row.LeftExecution != "" && row.SeriesExecution != "" && row.LeftExecution != row.SeriesExecution,
		}
	}
	if ignore := ledgerIgnoreFrom(row); ignore != nil {
		entry.Ignore = ignore
	}
	return entry, nil
}

// ledgerLastSeen is the series' latest completion for a present finding, or
// the finding's final event once it left.
func ledgerLastSeen(state api.FindingLedgerState, lastEvent time.Time, seriesCompleted string) time.Time {
	if state != api.FindingLedgerOpen && state != api.FindingLedgerReopened {
		return lastEvent
	}
	completed, err := db.ParseTime(seriesCompleted)
	if err != nil || completed.Before(lastEvent) {
		return lastEvent
	}
	return completed
}

// Keep lapsed ignore decisions to explain reopened findings.
func ledgerIgnoreFrom(row db.ListFindingLedgerRow) *api.FindingIgnore {
	if strings.TrimSpace(row.IgnoreEntryID) == "" {
		return nil
	}
	ignore := &api.FindingIgnore{
		EntryID:       row.IgnoreEntryID,
		Reason:        row.IgnoreReason,
		MatchedOn:     row.IgnoreMatchedOn,
		Justification: api.FindingIgnoreJustification(row.IgnoreJustification),
		ExpiresOn:     strings.TrimSpace(row.IgnoreExpires),
	}
	ignore.Expired = scanignore.IgnoreEntry{Expires: ignore.ExpiresOn}.Expired(time.Now().UTC())
	return ignore
}

// ledgerTotals counts the whole ledger, regardless of the query's filters.
func (s *SQLStore) ledgerTotals(ctx context.Context, canonicalPath string) (api.FindingLedgerCounts, map[string]int, error) {
	counts := api.FindingLedgerCounts{}
	byLevel := make(map[string]int, len(api.AllFindingLevelValues()))
	for _, level := range api.AllFindingLevelValues() {
		byLevel[string(level)] = 0
	}

	rows, err := s.queries.CountFindingLedgerTotals(ctx, canonicalPath)
	if err != nil {
		return counts, nil, fmt.Errorf("count finding ledger totals: %w", err)
	}
	for _, row := range rows {
		state, level, total := row.State, row.Level, int(row.Total)
		switch api.FindingLedgerState(state) {
		case api.FindingLedgerOpen:
			counts.Open += total
		case api.FindingLedgerReopened:
			counts.Reopened += total
		case api.FindingLedgerFixed:
			counts.Fixed += total
		case api.FindingLedgerNotObserved:
			counts.NotObserved += total
		case api.FindingLedgerUnverified:
			counts.Unverified += total
		case api.FindingLedgerIgnored:
			counts.Ignored += total
		}
		if _, known := byLevel[level]; known {
			byLevel[level] += total
		} else {
			byLevel[string(api.FindingLevelUnknown)] += total
		}
	}
	return counts, byLevel, nil
}

type ledgerOrder struct {
	key        api.FindingLedgerSort
	descending bool
}

func ledgerSortOrder(key api.FindingLedgerSort, order string) ledgerOrder {
	switch key {
	case api.FindingLedgerSortSeverity, api.FindingLedgerSortFinding,
		api.FindingLedgerSortLocation, api.FindingLedgerSortState,
		api.FindingLedgerSortFirstSeen, api.FindingLedgerSortLastSeen:
	default:
		key = api.FindingLedgerSortSeverity
	}
	order = strings.TrimSpace(order)
	if order == "" {
		order = "desc"
		if key == api.FindingLedgerSortFinding || key == api.FindingLedgerSortLocation {
			order = "asc"
		}
	}
	descending := strings.EqualFold(order, "desc")
	// Severity and state use lower ranks for more urgent findings.
	if key == api.FindingLedgerSortSeverity || key == api.FindingLedgerSortState {
		descending = !descending
	}
	return ledgerOrder{key: key, descending: descending}
}
