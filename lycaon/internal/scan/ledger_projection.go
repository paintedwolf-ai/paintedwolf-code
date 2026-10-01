package scan

import (
	"context"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/db"
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/pkg/api"
)

type ledgerRowFacts struct {
	Fingerprint string
	Level       string
	LevelRank   int64
	Kind        string
	RuleID      string
	Message     string
	URI         string
	StartLine   int64
	AdvisoryIDs string
	HintCode    string
	FindingJSON string
}

func ledgerFactsFor(finding api.SecurityFinding) (ledgerRowFacts, error) {
	raw, err := surveyjson.Marshal(finding)
	if err != nil {
		return ledgerRowFacts{}, err
	}
	level := finding.Level
	if level == "" {
		level = api.FindingLevelUnknown
	}
	uri, line := primaryLocation(finding)
	facts := ledgerRowFacts{
		Fingerprint: FindingIdentity(finding),
		Level:       string(level),
		LevelRank:   int64(api.FindingLevelRank(level)),
		RuleID:      finding.RuleID,
		Message:     finding.Message,
		URI:         uri,
		StartLine:   int64(line),
		FindingJSON: string(raw),
	}
	if props := finding.Properties; props != nil && props.Lycaon != nil {
		facts.Kind = string(props.Lycaon.Kind)
		facts.HintCode = props.Lycaon.HintCode
		facts.AdvisoryIDs = advisoryIDIndex(props.Lycaon.Advisory)
	}
	return facts, nil
}

// advisoryIDIndex space-delimits ids on both ends so LIKE matches whole ids.
func advisoryIDIndex(advisory *api.AdvisoryRef) string {
	ids := scanfindings.AdvisoryIDs(advisory)
	if len(ids) == 0 {
		return ""
	}
	sort.Strings(ids)
	return " " + strings.Join(ids, " ") + " "
}

func projectIntroduced(ctx context.Context, qtx *db.Queries, scan *api.CodeScan, finding api.SecurityFinding, observedAt string) error {
	facts, err := ledgerFactsFor(finding)
	if err != nil {
		return err
	}
	if facts.Fingerprint == "" {
		return nil
	}
	return qtx.UpsertScanFindingLedgerIntroduced(ctx, db.UpsertScanFindingLedgerIntroducedParams{
		CanonicalPath: scan.CanonicalPath,
		ScannerID:     scan.ScannerID,
		Fingerprint:   facts.Fingerprint,
		Level:         facts.Level,
		LevelRank:     facts.LevelRank,
		Kind:          facts.Kind,
		RuleID:        facts.RuleID,
		Message:       facts.Message,
		Uri:           facts.URI,
		StartLine:     facts.StartLine,
		AdvisoryIds:   facts.AdvisoryIDs,
		HintCode:      facts.HintCode,
		ObservedAt:    observedAt,
		ScanID:        scan.ID,
		SnapshotID:    scan.SourceSnapshotID,
		FindingJson:   facts.FindingJSON,
	})
}

func projectFixed(ctx context.Context, qtx *db.Queries, scan *api.CodeScan, finding api.SecurityFinding, observedAt string) error {
	fingerprint := FindingIdentity(finding)
	if fingerprint == "" {
		return nil
	}
	return qtx.MarkScanFindingLedgerFixed(ctx, db.MarkScanFindingLedgerFixedParams{
		ObservedAt:     observedAt,
		ScanID:         scan.ID,
		LeftTargetKind: string(scan.TargetKind),
		LeftCoverage:   string(scan.CoverageStatus),
		LeftExecution:  scan.ExecutionFingerprint,
		CanonicalPath:  scan.CanonicalPath,
		ScannerID:      scan.ScannerID,
		Fingerprint:    fingerprint,
	})
}
