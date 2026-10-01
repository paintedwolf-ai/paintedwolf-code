package scan

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/pkg/api"
)

const (
	findingEventIntroduced = "introduced"
	findingEventFixed      = "fixed"
)

func FindingIdentity(f api.SecurityFinding) string {
	return strings.TrimSpace(f.Fingerprints.Primary)
}

// Stable ordering makes writes independent of map iteration.
func SortFindingsByIdentity(findings []api.SecurityFinding) {
	sort.SliceStable(findings, func(i, j int) bool {
		return FindingIdentity(findings[i]) < FindingIdentity(findings[j])
	})
}

// History, ledger projection, and notification commit together.
func (s *SQLStore) RecordFindingEvents(ctx context.Context, scan *api.CodeScan, introduced, fixed []api.SecurityFinding, at time.Time) error {
	if s == nil || scan == nil || (len(introduced) == 0 && len(fixed) == 0) {
		return nil
	}
	stamp := db.FormatTime(at.UTC())
	return s.inTx(ctx, func(qtx *db.Queries, tx *sql.Tx) error {
		for _, batch := range []struct {
			event    string
			findings []api.SecurityFinding
		}{
			{findingEventIntroduced, introduced},
			{findingEventFixed, fixed},
		} {
			for _, finding := range batch.findings {
				id := FindingIdentity(finding)
				if id == "" {
					continue
				}
				raw, err := surveyjson.Marshal(finding)
				if err != nil {
					return err
				}
				if err := qtx.InsertScanFindingEvent(ctx, db.InsertScanFindingEventParams{
					CanonicalPath: scan.CanonicalPath, ScannerID: scan.ScannerID, Fingerprint: id,
					Event: batch.event, SnapshotID: scan.SourceSnapshotID, ScanID: scan.ID, ObservedAt: stamp, FindingJson: string(raw),
				}); err != nil {
					return err
				}
				if batch.event == findingEventIntroduced {
					err = projectIntroduced(ctx, qtx, scan, finding, stamp)
				} else {
					err = projectFixed(ctx, qtx, scan, finding, stamp)
				}
				if err != nil {
					return err
				}
			}
		}
		// Clear the applied ignore digest so the next read applies ignores to new rows.
		if err := qtx.PutScanIgnoreDigest(ctx, db.PutScanIgnoreDigestParams{
			CanonicalPath: scan.CanonicalPath,
			Digest:        "",
			AppliedAt:     stamp,
		}); err != nil {
			return err
		}
		return s.emitScanTx(ctx, tx, scan.ID)
	})
}

// Current presence survives repeated snapshots and includes ignored findings.
func (s *SQLStore) OpenFindings(ctx context.Context, canonicalPath, scannerID string) (map[string]api.SecurityFinding, error) {
	out := make(map[string]api.SecurityFinding)
	for offset := int64(0); ; offset += MaxLedgerPageSize {
		rows, err := s.queries.ListPresentScanFindings(ctx, db.ListPresentScanFindingsParams{
			CanonicalPath: canonicalPath, ScannerID: scannerID, PageLimit: MaxLedgerPageSize, PageOffset: offset,
		})
		if err != nil {
			return nil, fmt.Errorf("read present findings: %w", err)
		}
		for _, row := range rows {
			var finding api.SecurityFinding
			if err := json.Unmarshal([]byte(row.FindingJson), &finding); err != nil {
				return nil, fmt.Errorf("decode present finding %s: %w", row.Fingerprint, err)
			}
			out[row.Fingerprint] = finding
		}
		if len(rows) < MaxLedgerPageSize {
			return out, nil
		}
	}
}

// Results include the boundary timestamp and run newest first.
func (s *SQLStore) FixedFindingsSince(ctx context.Context, canonicalPath, scannerID string, since time.Time) ([]api.SecurityFinding, error) {
	rows, err := s.queries.ListScanFindingEventsSince(ctx, db.ListScanFindingEventsSinceParams{
		CanonicalPath: canonicalPath, ScannerID: scannerID, Event: findingEventFixed, ObservedAt: db.FormatTime(since.UTC()),
	})
	if err != nil {
		return nil, err
	}
	out := make([]api.SecurityFinding, 0, len(rows))
	for i := len(rows) - 1; i >= 0; i-- {
		var finding api.SecurityFinding
		if err := json.Unmarshal([]byte(rows[i].FindingJson), &finding); err != nil {
			continue
		}
		if at, err := db.ParseTime(rows[i].ObservedAt); err == nil {
			finding.History = &api.SecurityFindingHistory{IntroducedAt: at, IntroducedSnapshotID: rows[i].SnapshotID, IntroducedScanID: rows[i].ScanID}
		}
		out = append(out, finding)
	}
	return out, nil
}

func (s *SQLStore) attachFindingHistory(ctx context.Context, canonicalPath, scannerID string, findings []api.SecurityFinding) error {
	if len(findings) == 0 || strings.TrimSpace(scannerID) == "" {
		return nil
	}
	rows, err := s.queries.ListScanFindingIntroductions(ctx, db.ListScanFindingIntroductionsParams{
		CanonicalPath: canonicalPath, ScannerID: scannerID,
	})
	if err != nil {
		return err
	}
	byFingerprint := make(map[string]api.SecurityFindingHistory, len(rows))
	for _, row := range rows {
		at, ok := row.IntroducedAt.(string)
		if !ok {
			continue
		}
		when, err := db.ParseTime(at)
		if err != nil {
			continue
		}
		byFingerprint[row.Fingerprint] = api.SecurityFindingHistory{
			IntroducedAt: when, IntroducedSnapshotID: row.SnapshotID, IntroducedScanID: row.ScanID,
		}
	}
	for i := range findings {
		if history, ok := byFingerprint[FindingIdentity(findings[i])]; ok {
			h := history
			findings[i].History = &h
		}
	}
	return nil
}

// BlobFindings reads the findings cached for one file's bytes, named by the
// manifest entry's content id, under one execution identity.
func (s *SQLStore) BlobFindings(ctx context.Context, executionFingerprint, contentID, targetPath string) ([]api.SecurityFinding, bool, error) {
	raw, err := s.queries.GetScanBlobFindings(ctx, db.GetScanBlobFindingsParams{
		ExecutionFingerprint: executionFingerprint, ContentID: contentID, TargetPath: targetPath,
	})
	if db.IsNoRows(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var findings []api.SecurityFinding
	if err := json.Unmarshal([]byte(raw), &findings); err != nil {
		return nil, false, err
	}
	return findings, true, nil
}

// CachedFileResult includes the path because rule applicability may depend on it.
type CachedFileResult struct {
	ContentID string
	Path      string
	Findings  []api.SecurityFinding
}

// SaveBlobFindings commits a bounded page, including verified clean results.
func (s *SQLStore) SaveBlobFindings(ctx context.Context, executionFingerprint string, files []CachedFileResult, at time.Time) error {
	return s.inTx(ctx, func(q *db.Queries, _ *sql.Tx) error {
		for _, file := range files {
			findings := file.Findings
			if findings == nil {
				findings = []api.SecurityFinding{}
			}
			SortFindingsByIdentity(findings)
			raw, err := surveyjson.Marshal(findings)
			if err != nil {
				return err
			}
			if err := q.UpsertScanBlobFindings(ctx, db.UpsertScanBlobFindingsParams{
				ExecutionFingerprint: executionFingerprint, ContentID: file.ContentID, TargetPath: file.Path,
				FindingsJson: string(raw), CreatedAt: db.FormatTime(at.UTC()),
			}); err != nil {
				return err
			}
		}
		return nil
	})
}

// IntroducedSince counts the findings a project's series introduced at or
// after since with a level at or above minLevel.
func (s *SQLStore) IntroducedSince(ctx context.Context, canonicalPath string, since time.Time, minLevel api.FindingLevel) (int, error) {
	rows, err := s.queries.ListScanFindingIntroductionsSince(ctx, db.ListScanFindingIntroductionsSinceParams{
		CanonicalPath: canonicalPath, ObservedAt: db.FormatTime(since.UTC()),
	})
	if err != nil {
		return 0, err
	}
	count := 0
	for _, row := range rows {
		var finding api.SecurityFinding
		if err := json.Unmarshal([]byte(row.FindingJson), &finding); err != nil {
			continue
		}
		// Lower rank is more severe.
		if api.FindingLevelRank(finding.Level) <= api.FindingLevelRank(minLevel) {
			count++
		}
	}
	return count, nil
}
