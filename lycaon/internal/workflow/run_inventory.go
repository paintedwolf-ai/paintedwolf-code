package workflow

import (
	"context"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/scan"
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
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

// ScanGroupCheck is how a set of cited group ids stands against a run's
// inventory.
type ScanGroupCheck struct {
	// Unknown ids name no group in the settled inventory.
	Unknown []string
	// ScanIDs are cited ids that name a scan, with that scan's groups the run
	// inventory holds: the ids the citation should have used.
	ScanIDs map[string][]string
}

// OK reports whether every cited id names a group the run holds.
func (c ScanGroupCheck) OK() bool { return len(c.Unknown) == 0 && len(c.ScanIDs) == 0 }

// CheckScanGroups classifies cited group ids against the run's inventory. An
// id that names a scan is always wrong; an id that names nothing is wrong once
// the bound scans are terminal.
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
	if byField, err := ParseVerdictClaims(def, verdict); err == nil {
		for _, field := range sortedClaimFields(byField) {
			for _, c := range byField[field] {
				out = append(out, c.ScanGroupIDs...)
			}
		}
	}
	if setAsides, err := ParseVerdictSetAsides(def, verdict); err == nil {
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
	account := scanfindings.AccountInventory(run.Groups, nil, setAsideSelectors(setAsides))
	var out []string
	for i, n := range account.SetAsideCounts {
		if n == 0 {
			out = append(out, strings.TrimSpace(setAsides[i].Reason))
		}
	}
	return out, nil
}

func setAsideSelectors(in []guidance.CoordinatorSetAside) []scanfindings.SetAside {
	out := make([]scanfindings.SetAside, 0, len(in))
	for _, sa := range in {
		out = append(out, scanfindings.SetAside{GroupIDs: sa.ScanGroupIDs, Scanner: sa.Scanner, Paths: sa.Paths, Reason: sa.Reason})
	}
	return out
}

// ActiveRunOwnsScanEvidence reports whether the session's active run declares
// a scan obligation. Such a run's bound scans are its scan evidence, so scans
// the scan plane runs around it are no concern of its review.
func (m *RunManager) ActiveRunOwnsScanEvidence(ctx context.Context, sessionID string) bool {
	if m == nil {
		return false
	}
	run, err := m.Store.ActiveBySession(ctx, sessionID)
	if err != nil || run == nil {
		return false
	}
	manifest, err := m.manifestForRun(ctx, run)
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
func (m *RunManager) checkReviewInventory(ctx context.Context, run *api.WorkflowRun, def workflowdef.ReviewLoopDef, verdict map[string]string) (*guidance.ReportDocumentIssue, error) {
	if !def.RequireInventoryAccounted {
		return nil, nil
	}
	inventory, err := LoadRunInventory(ctx, m.Inventory, run.ID)
	if err != nil {
		return nil, err
	}
	if !inventory.Settled {
		return &guidance.ReportDocumentIssue{Code: SubmitVerdictScansPendingCode}, nil
	}
	manifest, err := m.manifestForRun(ctx, run)
	if err != nil {
		return nil, err
	}
	var phases []PhaseVerdict
	for _, prior := range ReviewVerdicts(ctx, m, run, manifest) {
		if prior.Phase != run.CurrentPhase {
			phases = append(phases, prior)
		}
	}
	phases = append(phases, PhaseVerdict{
		Phase: run.CurrentPhase, Def: def,
		Record: evidence.Record{Artifacts: verdictArtifacts(verdict, nil, nil)},
	})
	issue := checkInventoryAccounted(guidance.CoordinatorCompletionReport{}, ReportDocumentFacts{
		Claims: ReconcileClaims(phases), SetAsides: RunSetAsides(phases), Inventory: inventory,
	})
	if issue.Code == "" {
		return nil, nil
	}
	return &issue, nil
}
