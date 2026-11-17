package conditions

import (
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

func baselineScanComplete(deps CoreDeps, ec EvalContext) (bool, error) {
	scan, _, err := latestProactiveCompleteScan(deps, ec)
	if err != nil || scan == nil {
		return false, err
	}
	return scan.Status == api.CodeScanStatusComplete, nil
}

func noCriticalFindings(deps CoreDeps, ec EvalContext) (bool, error) {
	if deps.SecurityScannersEnabled != nil && !deps.SecurityScannersEnabled() {
		return true, nil
	}
	sessionID := strings.TrimSpace(ec.SessionID)
	if deps.ScanLedger == nil || sessionID == "" {
		return false, nil
	}
	scan, err := deps.ScanLedger.LatestRequiredScanForSession(ec.Ctx, sessionID)
	if err != nil || scan == nil {
		return scan == nil && err == nil, err
	}
	if scan.Status != api.CodeScanStatusComplete {
		return false, nil
	}
	return !scanHasFindingAtOrAboveLevel(scan, minLevelFromEvalContext(ec)), nil
}

func minLevelFromEvalContext(ec EvalContext) api.FindingLevel {
	if ec.Vars != nil {
		if raw, ok := ec.Vars["min_level"].(string); ok && strings.TrimSpace(raw) != "" {
			return api.ParseFindingLevel(raw)
		}
		if args, ok := ec.Vars["args"].(map[string]any); ok {
			if raw, ok := args["min_level"].(string); ok && strings.TrimSpace(raw) != "" {
				return api.ParseFindingLevel(raw)
			}
		}
	}
	return api.FindingLevelHigh
}

func scanHasFindingAtOrAboveLevel(scan *api.CodeScan, floor api.FindingLevel) bool {
	if scan == nil {
		return false
	}
	if scan.FindingsByLevel != nil {
		for level, count := range scan.FindingsByLevel {
			if count > 0 && api.FindingLevelAtOrAbove(api.ParseFindingLevel(level), floor) {
				return true
			}
		}
	}
	for _, f := range scan.Findings {
		if api.FindingLevelAtOrAbove(f.Level, floor) {
			return true
		}
	}
	return false
}

func latestProactiveCompleteScan(deps CoreDeps, ec EvalContext) (*api.CodeScan, string, error) {
	sessionID := strings.TrimSpace(ec.SessionID)
	if deps.ScanLedger == nil || sessionID == "" {
		return nil, "", nil
	}
	snapshotID, err := currentScanIdentity(deps, ec)
	if err != nil {
		return nil, "", err
	}
	scan, err := deps.ScanLedger.LatestCompleteForSessionSnapshot(ec.Ctx, sessionID, snapshotID, proactiveCategories(deps))
	if err != nil {
		return nil, "", err
	}
	return scan, snapshotID, nil
}
