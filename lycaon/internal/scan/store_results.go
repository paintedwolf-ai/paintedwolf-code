package scan

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/evidence"
	scanignore "github.com/lycaon/lycaon/internal/scan/ignores"
	"github.com/lycaon/lycaon/pkg/api"
)

// SaveIngest persists bounded guidance and the normalized ingest record.
func (s *SQLStore) SaveIngest(ctx context.Context, id string, rec evidence.Record) error {
	guidanceJSON, ingestJSON, err := marshalIngest(rec)
	if err != nil {
		return err
	}
	return s.inTx(ctx, func(qtx *db.Queries, tx *sql.Tx) error {
		if err := qtx.SaveScanIngest(ctx, db.SaveScanIngestParams{
			GuidanceJson: db.NullString(guidanceJSON),
			IngestJson:   db.NullString(ingestJSON),
			ID:           id,
		}); err != nil {
			return err
		}
		return s.emitScanTx(ctx, tx, id)
	})
}

// LoadPaths reads paths_json for a scan id.
func (s *SQLStore) LoadPaths(ctx context.Context, id string) ([]string, error) {
	pathsJSON, err := s.queries.GetScanPathsJSON(ctx, id)
	if db.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if pathsJSON == "" {
		return nil, nil
	}
	var paths []string
	if err := json.Unmarshal([]byte(pathsJSON), &paths); err != nil {
		return nil, err
	}
	return paths, nil
}

func uniqueNonEmpty(paths []string) []string {
	seen := make(map[string]struct{}, len(paths))
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out
}

func marshalIngest(rec evidence.Record) (guidanceJSON, ingestJSON string, err error) {
	if rec.Artifacts == nil {
		return "", "", nil
	}
	payload, err := json.Marshal(rec.Artifacts)
	if err != nil {
		return "", "", err
	}
	var parsed struct {
		Guidance          []api.ScanGuidanceSummary   `json:"guidance"`
		Findings          []api.SecurityFinding       `json:"findings"`
		Ignored           []scanignore.IgnoredFinding `json:"ignored"`
		FindingsCount     int                         `json:"findings_count"`
		FindingsStored    int                         `json:"findings_stored"`
		FindingsMerged    int                         `json:"findings_merged"`
		FindingsByLevel   map[string]int              `json:"findings_by_level"`
		AgentBudget       *api.ScanAgentBudget        `json:"agent_budget"`
		ScanScope         string                      `json:"scan_scope"`
		SarifStatistics   map[string]any              `json:"sarif_statistics"`
		Warnings          []api.ScanWarning           `json:"warnings"`
		CoverageStatus    api.ScanCoverageStatus      `json:"coverage_status"`
		GuidanceTruncated bool                        `json:"guidance_truncated"`
	}
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return "", "", err
	}
	gb, err := json.Marshal(parsed.Guidance)
	if err != nil {
		return "", "", err
	}
	stored := len(parsed.Findings)
	if parsed.FindingsStored > 0 {
		stored = parsed.FindingsStored
	}
	ib, err := json.Marshal(IngestArtifacts{
		Findings:          parsed.Findings,
		Ignored:           parsed.Ignored,
		FindingsCount:     parsed.FindingsCount,
		FindingsStored:    stored,
		FindingsMerged:    parsed.FindingsMerged,
		FindingsByLevel:   parsed.FindingsByLevel,
		AgentBudget:       parsed.AgentBudget,
		ScanScope:         parsed.ScanScope,
		SarifStatistics:   parsed.SarifStatistics,
		Warnings:          parsed.Warnings,
		CoverageStatus:    parsed.CoverageStatus,
		GuidanceTruncated: parsed.GuidanceTruncated,
	})
	if err != nil {
		return "", "", err
	}
	return string(gb), string(ib), nil
}
