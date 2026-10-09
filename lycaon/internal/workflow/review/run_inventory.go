package review

import (
	"context"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/scan"
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowpresentation "github.com/lycaon/lycaon/internal/workflow/presentation"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	workflowvalidation "github.com/lycaon/lycaon/internal/workflow/validation"
	"github.com/lycaon/lycaon/pkg/api"
)

// SubmitVerdictScanGroupUnknownCode rejects a verdict whose claims cite
// scanner groups outside the run's inventory.
const SubmitVerdictScanGroupUnknownCode = "SUBMIT_VERDICT_SCAN_GROUP_UNKNOWN"

const SubmitVerdictScansPendingCode = "SUBMIT_VERDICT_SCANS_PENDING"

// ScanInventory reads the scans a run's review is accountable for.
type ScanInventory interface {
	// RunScans are the scans bound to a run, with their stored findings.
	RunScans(ctx context.Context, runID string) ([]api.CodeScan, error)
	// Scan reads any scan by id; nil when no scan has the id.
	Scan(ctx context.Context, scanID string) (*api.CodeScan, error)
}

// RunInventory is a run's scanner groups, and whether every bound scan is
// terminal, so an id outside them cannot still appear.
type RunInventory struct {
	Groups  []scanfindings.InventoryGroup
	Settled bool
	Scans   []api.CodeScan
}

// LoadRunInventory reads and groups a run's bound scans. A run with no bound
// scans has an empty, settled inventory.
func LoadRunInventory(ctx context.Context, inv ScanInventory, runID string) (RunInventory, error) {
	out := RunInventory{Settled: true}
	if inv == nil || strings.TrimSpace(runID) == "" {
		return out, nil
	}
	scans, err := inv.RunScans(ctx, runID)
	if err != nil {
		return out, err
	}
	out.Scans = scans
	for _, s := range scans {
		if !scan.StatusTerminal(s.Status) {
			out.Settled = false
		}
	}
	out.Groups = scanfindings.InventoryGroups(scanfindings.InventoryFindings(scans))
	return out, nil
}

// ScanGroupCheck identifies citations outside the run's scanner groups.
type ScanGroupCheck struct {
	// Unknown ids name no group in the settled inventory.
	Unknown []string
	// ScanIDs maps misused scan ids to their groups in this run.
	ScanIDs map[string][]string
}

// OK reports whether every cited id names a group the run holds.
func (c ScanGroupCheck) OK() bool { return len(c.Unknown) == 0 && len(c.ScanIDs) == 0 }

// Unknown group ids become invalid once the run's scans settle.
func CheckScanGroups(ctx context.Context, inv ScanInventory, runID string, cited []string) (ScanGroupCheck, error) {
	var out ScanGroupCheck
	if inv == nil || len(cited) == 0 {
		return out, nil
	}
	run, err := LoadRunInventory(ctx, inv, runID)
	if err != nil {
		return out, err
	}
	held := make(map[string]bool, len(run.Groups))
	for _, g := range run.Groups {
		held[g.ID] = true
	}
	seen := map[string]bool{}
	for _, id := range cited {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] || held[id] {
			continue
		}
		seen[id] = true
		named, err := inv.Scan(ctx, id)
		if err != nil {
			return out, err
		}
		if named != nil {
			if out.ScanIDs == nil {
				out.ScanIDs = map[string][]string{}
			}
			var ids []string
			for _, g := range scanfindings.InventoryGroups(scanfindings.InventoryFindings([]api.CodeScan{*named})) {
				if held[g.ID] {
					ids = append(ids, g.ID)
				}
			}
			out.ScanIDs[id] = ids
			continue
		}
		if run.Settled {
			out.Unknown = append(out.Unknown, id)
		}
	}
	sort.Strings(out.Unknown)
	return out, nil
}

// VerdictScanGroups lists every scanner group id a verdict names: those its
// claims cite and those its set-asides name.
func VerdictScanGroups(def workflowdef.ReviewLoopDef, verdict map[string]string) []string {
	var out []string
	if byField, err := workflowvalidation.ParseVerdictClaims(def, verdict); err == nil {
		for _, field := range workflowvalidation.SortedClaimFields(byField) {
			for _, c := range byField[field] {
				out = append(out, c.ScanGroupIDs...)
			}
		}
	}
	if setAsides, err := workflowvalidation.ParseVerdictSetAsides(def, verdict); err == nil {
		for _, sa := range setAsides {
			out = append(out, sa.ScanGroupIDs...)
		}
	}
	return out
}

// EmptySetAsides are the reasons of set-asides that select no group of the
// run's settled inventory; before the inventory settles every selector counts.
func EmptySetAsides(ctx context.Context, inv ScanInventory, runID string, setAsides []guidance.CoordinatorSetAside) ([]string, error) {
	if inv == nil || len(setAsides) == 0 {
		return nil, nil
	}
	run, err := LoadRunInventory(ctx, inv, runID)
	if err != nil || !run.Settled {
		return nil, err
	}
	account := scanfindings.AccountInventory(run.Groups, nil, workflowpresentation.SetAsideSelectors(setAsides))
	var out []string
	for i, n := range account.SetAsideCounts {
		if n == 0 {
			out = append(out, strings.TrimSpace(setAsides[i].Reason))
		}
	}
	return out, nil
}

// ActiveRunOwnsScanEvidence reports whether the session's active run declares
// a scan obligation. Such a run's bound scans are its scan evidence, so scans
// the scan plane runs around it are no concern of its review.
func (m *Coverage) ActiveRunOwnsScanEvidence(ctx context.Context, sessionID string) bool {
	if m == nil {
		return false
	}
	run, err := m.Runs.ActiveBySession(ctx, sessionID)
	if err != nil || run == nil {
		return false
	}
	manifest, err := m.Resolver.ForRun(ctx, run)
	if err != nil {
		return false
	}
	for _, phase := range manifest.PhaseDefs {
		for _, o := range phase.OnEnter.Obligations {
			if o.Kind == scan.WorkflowObligationKind {
				return true
			}
		}
	}
	return false
}

// checkReviewInventory checks the proposed verdict before its phase can settle.
func (m *Coverage) checkReviewInventory(ctx context.Context, run *api.WorkflowRun, def workflowdef.ReviewLoopDef, verdict map[string]string) (*runstate.InventoryIssue, error) {
	if !def.RequireInventoryAccounted {
		return nil, nil
	}
	inventory, err := LoadRunInventory(ctx, m.Inventory, run.ID)
	if err != nil {
		return nil, err
	}
	if !inventory.Settled {
		return &runstate.InventoryIssue{ReportDocumentIssue: guidance.ReportDocumentIssue{Code: SubmitVerdictScansPendingCode}}, nil
	}
	manifest, err := m.Resolver.ForRun(ctx, run)
	if err != nil {
		return nil, err
	}
	priors, err := workflowpresentation.ReviewVerdicts(ctx, m.Reviews, run, manifest)
	if err != nil {
		return nil, err
	}
	var phases []workflowpresentation.PhaseVerdict
	for _, prior := range priors {
		if prior.Phase != run.CurrentPhase {
			phases = append(phases, prior)
		}
	}
	phases = append(phases, workflowpresentation.PhaseVerdict{
		Phase: run.CurrentPhase, Def: def,
		Record: evidence.Record{Artifacts: verdictArtifacts(verdict, nil, nil)},
	})
	issue, unaccounted := inventoryAccounting(guidance.CoordinatorCompletionReport{}, ReportDocumentFacts{
		Claims: workflowpresentation.ReconcileClaims(phases), SetAsides: workflowpresentation.RunSetAsides(phases), Inventory: inventory,
	})
	if issue.Code == "" {
		return nil, nil
	}
	out := &runstate.InventoryIssue{ReportDocumentIssue: issue, Unaccounted: unaccounted}
	if len(unaccounted) > 0 {
		out.Regressed = m.noteInventoryShortfall(run.ID, run.CurrentPhase, unaccounted)
	}
	return out, nil
}
