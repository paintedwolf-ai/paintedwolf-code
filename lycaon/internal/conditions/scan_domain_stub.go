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
	if deps.ScanLedger == nil {
		return false, nil
	}
	dep, depErr := loadDelegation(deps, ec)
	if depErr != nil {
		return false, depErr
	}
	if dep != nil {
		scan, err := deps.ScanLedger.LatestRequiredScanForDelegation(ec.Ctx, dep.ID)
		if err != nil || scan == nil {
			return scan == nil, err
		}
		if scan.Status != api.CodeScanStatusComplete {
			return false, nil
		}
		return !scanHasFindingAtOrAboveLevel(scan, minLevelFromEvalContext(ec)), nil
	}
	return true, nil
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
	if deps.ScanLedger == nil {
		return nil, "", nil
	}
	dep, err := loadDelegation(deps, ec)
	if err != nil || dep == nil {
		return nil, "", err
	}
	snapshotID, err := currentScanIdentity(deps, ec)
	if err != nil {
		return nil, "", err
	}
	scan, err := deps.ScanLedger.LatestCompleteForDelegationSnapshot(ec.Ctx, dep.ID, snapshotID, proactiveCategories(deps))
	if err != nil {
		return nil, "", err
	}
	return scan, snapshotID, nil
}
