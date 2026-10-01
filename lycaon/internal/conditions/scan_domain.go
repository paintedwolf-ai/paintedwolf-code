package conditions

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/inspector"
	"github.com/lycaon/lycaon/pkg/api"
)

// ScanLedger reads async code scan rows for scan-domain predicates.
type ScanLedger interface {
	FindInFlightForDelegationSnapshot(ctx context.Context, delegationID, snapshotID string, categories []api.ScanCategory, paths []string) (*api.CodeScan, error)
	LatestCompleteForDelegationSnapshot(ctx context.Context, delegationID, snapshotID string, categories []api.ScanCategory) (*api.CodeScan, error)
	LatestRequiredScanForDelegation(ctx context.Context, delegationID string) (*api.CodeScan, error)
}

// ShippedScanDomainIDs returns the registered scan-domain predicates.
func ShippedScanDomainIDs() []string {
	return append([]string(nil), shippedScanDomainIDs...)
}

var shippedScanDomainIDs = []string{
	"scan_pending",
	"scan_stale",
	"required_scans_enqueued",
	"lint_gate_passed",
	"baseline_scan_complete",
	"security_evidence_fresh",
	"no_critical_findings",
}

// ScanCatalogStubIDs returns deferred scan predicates. Bundled workflows must not use them as gates.
func ScanCatalogStubIDs() []string {
	return append([]string(nil), scanCatalogStubIDs...)
}

var scanCatalogStubIDs = []string{
	"findings_triaged",
	"remediation_complete",
}

// RegisterScanDomain registers scan-workflow vocabulary evaluators.
func RegisterScanDomain(reg *ConditionRegistry, deps RegistryDeps) error {
	if reg == nil {
		return nil
	}
	core := CoreDeps{
		DelegationStore:         deps.DelegationStore,
		Evidence:                deps.Evidence,
		ScanLedger:              deps.ScanLedger,
		SourceSnapshots:         deps.SourceSnapshots,
		ScanProactiveCategories: deps.ScanProactiveCategories,
		SecurityScannersEnabled: deps.SecurityScannersEnabled,
	}
	entries := []struct {
		name string
		fn   ConditionFunc
	}{
		{"scan_pending", func(ec EvalContext) (bool, error) {
			return scanPending(core, ec)
		}},
		{"scan_stale", func(ec EvalContext) (bool, error) {
			return scanStale(core, ec)
		}},
		{"required_scans_enqueued", func(ec EvalContext) (bool, error) {
			return requiredScansEnqueued(core, ec)
		}},
		{"lint_gate_passed", func(ec EvalContext) (bool, error) {
			return lintGatePassed(core, ec)
		}},
		{"baseline_scan_complete", func(ec EvalContext) (bool, error) {
			return baselineScanComplete(core, ec)
		}},
		{"security_evidence_fresh", func(ec EvalContext) (bool, error) {
			return securityEvidenceSatisfied(core, ec, true)
		}},
		{"no_critical_findings", func(ec EvalContext) (bool, error) {
			return noCriticalFindings(core, ec)
		}},
	}
	for _, e := range entries {
		if reg.Has(e.name) {
			continue
		}
		if err := reg.Register(e.name, e.fn); err != nil {
			return err
		}
	}
	return nil
}

func scanPending(deps CoreDeps, ec EvalContext) (bool, error) {
	if deps.ScanLedger == nil {
		return false, nil
	}
	dep, err := loadDelegation(deps, ec)
	if err != nil || dep == nil {
		return false, err
	}
	scan, err := deps.ScanLedger.LatestRequiredScanForDelegation(ec.Ctx, dep.ID)
	if err != nil || scan == nil {
		return false, err
	}
	return scan.Status == api.CodeScanStatusPending || scan.Status == api.CodeScanStatusRunning, nil
}

func scanStale(deps CoreDeps, ec EvalContext) (bool, error) {
	pending, err := scanPending(deps, ec)
	if err != nil || pending {
		return false, err
	}
	ok, err := securityEvidenceSatisfied(deps, ec, true)
	return !ok && err == nil, err
}

func requiredScansEnqueued(deps CoreDeps, ec EvalContext) (bool, error) {
	if deps.ScanLedger == nil {
		return false, nil
	}
	dep, err := loadDelegation(deps, ec)
	if err != nil || dep == nil {
		return false, err
	}
	_, err = deps.ScanLedger.LatestRequiredScanForDelegation(ec.Ctx, dep.ID)
	return err == nil, err
}

func lintGatePassed(deps CoreDeps, ec EvalContext) (bool, error) {
	if deps.ScanLedger == nil {
		return false, nil
	}
	dep, err := loadDelegation(deps, ec)
	if err != nil || dep == nil {
		return false, err
	}
	snapshotID, err := currentScanIdentity(deps, ec)
	if err != nil {
		return false, err
	}
	lintCats := []api.ScanCategory{api.ScanCategoryLint}
	inflight, err := deps.ScanLedger.FindInFlightForDelegationSnapshot(ec.Ctx, dep.ID, snapshotID, lintCats, nil)
	if err != nil {
		return false, err
	}
	if inflight != nil {
		return false, nil
	}
	scan, err := deps.ScanLedger.LatestCompleteForDelegationSnapshot(ec.Ctx, dep.ID, snapshotID, lintCats)
	if err != nil || scan == nil {
		return false, err
	}
	if scan.Status == api.CodeScanStatusFailed {
		return false, nil
	}
	return !scanHasBlockingError(scan), nil
}

func securityEvidenceSatisfied(deps CoreDeps, ec EvalContext, requireAnchored bool) (bool, error) {
	if deps.SecurityScannersEnabled != nil && !deps.SecurityScannersEnabled() {
		return true, nil
	}
	if deps.ScanLedger == nil {
		return false, nil
	}
	dep, err := loadDelegation(deps, ec)
	if err != nil || dep == nil {
		return false, err
	}
	scan, err := deps.ScanLedger.LatestRequiredScanForDelegation(ec.Ctx, dep.ID)
	if err != nil {
		return false, err
	}
	if scan == nil {
		return true, nil
	}
	if scan.Status != api.CodeScanStatusComplete || deps.Evidence == nil {
		return false, nil
	}

	rec, err := deps.Evidence.LatestEvidence(ec.Ctx, ec.ProjectDir, dep.ID, api.ScanTaskID(scan.ID), evidence.GateTypeSecurity, inspector.EvidenceScope{})
	if err != nil {
		return false, err
	}
	if rec == nil {
		return false, nil
	}
	if requireAnchored {
		ok, _ := inspector.SecurityEvidenceMatchesSnapshot(*rec, scan.SourceSnapshotID)
		return ok, nil
	}
	return true, nil
}

func proactiveCategories(deps CoreDeps) []api.ScanCategory {
	if len(deps.ScanProactiveCategories) > 0 {
		return deps.ScanProactiveCategories
	}
	return []api.ScanCategory{api.ScanCategorySecurity, api.ScanCategorySecret}
}

func currentScanIdentity(deps CoreDeps, ec EvalContext) (string, error) {
	if deps.SourceSnapshots == nil || strings.TrimSpace(ec.ProjectDir) == "" {
		return "", fmt.Errorf("scan condition requires source snapshot authority")
	}
	id, err := deps.SourceSnapshots.SourceSnapshotID(ec.Ctx, ec.ProjectDir)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(id) == "" {
		return "", fmt.Errorf("scan condition received an empty source snapshot identity")
	}
	return id, nil
}

func scanHasBlockingError(scan *api.CodeScan) bool {
	if scan == nil {
		return false
	}
	for _, g := range scan.Guidance {
		if strings.EqualFold(strings.TrimSpace(g.Severity), "error") {
			return true
		}
	}
	return false
}
