package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"slices"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/scan"
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
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
func (m *RunManager) checkReviewInventory(ctx context.Context, run *api.WorkflowRun, def workflowdef.ReviewLoopDef, verdict map[string]string) (*InventoryIssue, error) {
	if !def.RequireInventoryAccounted {
		return nil, nil
	}
	inventory, err := LoadRunInventory(ctx, m.Inventory, run.ID)
	if err != nil {
		return nil, err
	}
	if !inventory.Settled {
		return &InventoryIssue{ReportDocumentIssue: guidance.ReportDocumentIssue{Code: SubmitVerdictScansPendingCode}}, nil
	}
	manifest, err := m.manifestForRun(ctx, run)
	if err != nil {
		return nil, err
	}
	var phases []PhaseVerdict
	verdicts, err := ReviewVerdicts(ctx, m, run, manifest)
	if err != nil {
		return nil, err
	}
	for _, prior := range verdicts {
		if prior.Phase != run.CurrentPhase {
			phases = append(phases, prior)
		}
	}
	phases = append(phases, PhaseVerdict{
		Phase: run.CurrentPhase, Def: def,
		Record: evidence.Record{Artifacts: verdictArtifacts(verdict, nil, nil)},
	})
	issue, unaccounted := inventoryAccounting(guidance.CoordinatorCompletionReport{}, ReportDocumentFacts{
		Claims: ReconcileClaims(phases), SetAsides: RunSetAsides(phases), Inventory: inventory,
	})
	if issue.Code == "" {
		return nil, nil
	}
	out := &InventoryIssue{ReportDocumentIssue: issue, Unaccounted: unaccounted}
	if len(unaccounted) > 0 {
		out.Regressed = m.noteInventoryShortfall(run.ID, run.CurrentPhase, unaccounted)
	}
	return out, nil
}

// InventoryAccounting answers scan_query's accounting views from a run's
// accepted evidence and its latest candidate submission.
type InventoryAccounting struct{ *RunManager }

type inventoryAccountRow struct {
	ID             string `json:"id"`
	Status         string `json:"status"`
	Selected       bool   `json:"selected"`
	Locationless   bool   `json:"locationless"`
	Candidate      bool   `json:"candidate"`
	MixedLocations bool   `json:"mixed_locations"`
}

// QueryWorkflowInventory projects accounting from accepted run evidence. A
// selector previews complete group locations without changing their disposition.
func (m InventoryAccounting) QueryWorkflowInventory(ctx context.Context, sessionID string, args map[string]any) (string, error) {
	for key := range args {
		switch key {
		case "scan_ids", "view", "inventory_revision", "selector", "offset", "limit", "project_dir":
		default:
			return "", &toolrejection.ToolReject{Code: "TOOL_ARGS_INVALID", Data: map[string]any{"tool": "scan_query", "field": key, "reason": "accounting_requires_complete_inventory"}}
		}
	}
	run, err := m.Store.ActiveBySession(ctx, sessionID)
	if err != nil {
		return "", err
	}
	if run == nil {
		return "", &toolrejection.ToolReject{Code: SubmitVerdictUnavailableCode, Data: map[string]any{"reason": "no_active_workflow_run"}}
	}
	unlock := m.lockRunVars(run.ID)
	defer unlock()
	run, err = m.loadRun(ctx, run.ID)
	if err != nil {
		return "", err
	}
	if m.Inventory == nil {
		return "", fmt.Errorf("workflow scan ledger unavailable")
	}
	manifest, err := m.manifestForRun(ctx, run)
	if err != nil {
		return "", err
	}
	inventory, err := LoadRunInventory(ctx, m.Inventory, run.ID)
	if err != nil {
		return "", err
	}
	if !inventory.Settled {
		return "", &toolrejection.ToolReject{Code: SubmitVerdictScansPendingCode}
	}
	var ids []string
	for _, scan := range inventory.Scans {
		ids = append(ids, scan.ID)
	}
	slices.Sort(ids)
	var requested []string
	switch supplied := args["scan_ids"].(type) {
	case []string:
		requested = append(requested, supplied...)
	case []any:
		for _, id := range supplied {
			if value, ok := id.(string); ok {
				requested = append(requested, value)
			}
		}
	}
	slices.Sort(requested)
	requested = slices.Compact(requested)
	if !slices.Equal(ids, requested) {
		return "", &toolrejection.ToolReject{Code: "TOOL_ARGS_INVALID", Data: map[string]any{"field": "scan_ids", "reason": "run_bound_scan_set_required"}}
	}
	revision, err := scanfindings.InventoryRevision(inventory.Scans)
	if err != nil {
		return "", err
	}
	if expected, _ := args["inventory_revision"].(string); expected != "" && expected != revision {
		return "", &toolrejection.ToolReject{Code: "SCAN_INVENTORY_STALE", Data: map[string]any{"inventory_revision": revision}}
	}
	verdicts, err := ReviewVerdicts(ctx, m.RunManager, run, manifest)
	if err != nil {
		return "", err
	}
	var linked []string
	for _, claim := range ReconcileClaims(verdicts) {
		linked = append(linked, claim.ScanGroupIDs...)
	}
	account := scanfindings.AccountInventory(inventory.Groups, linked, RunSetAsides(verdicts))
	candidate, candidateState, err := m.candidateInventory(ctx, run, manifest, inventory)
	if err != nil {
		return "", err
	}
	selector := scanfindings.SetAside{}
	if raw, ok := args["selector"].(map[string]any); ok {
		selector.Scanner, _ = raw["scanner"].(string)
		if paths, ok := raw["paths"].([]any); ok {
			for _, p := range paths {
				if path, ok := p.(string); ok {
					selector.Paths = append(selector.Paths, path)
				}
			}
		}
	}
	rows := make([]inventoryAccountRow, 0, len(inventory.Groups))
	selected, locationless, mixed := 0, 0, 0
	for _, g := range inventory.Groups {
		status := "unaccounted"
		if account.Linked[g.ID] {
			status = "accepted_claim"
		} else if _, ok := account.SetAsideBy[g.ID]; ok {
			status = "accepted_set_aside"
		}
		matches := selector.HasSelector() && selector.Selects(g)
		if matches {
			selected++
		}
		if len(g.Paths) == 0 {
			locationless++
		}
		partial := selectorPartlyMatches(selector, g)
		if partial {
			mixed++
		}
		_, candidateAside := candidate.SetAsideBy[g.ID]
		rows = append(rows, inventoryAccountRow{g.ID, status, matches, len(g.Paths) == 0, candidate.Linked[g.ID] || candidateAside, partial})
	}
	offset := max(0, inventoryInt(args, "offset", 0))
	offset = min(offset, len(rows))
	end := min(offset+min(max(1, inventoryInt(args, "limit", 50)), 100), len(rows))
	out := map[string]any{"scan_ids": ids, "inventory_revision": revision, "workflow_revision": run.Revision, "total_groups": len(rows), "candidate_state": candidateState, "candidate_groups": candidate.LinkedCount() + candidate.SetAsideCount(), "mixed_location_groups": mixed, "accepted_claim_groups": account.LinkedCount(), "accepted_set_aside_groups": account.SetAsideCount(), "unaccounted_groups": len(account.Unaccounted()), "selected_groups": selected, "locationless_groups": locationless, "groups": rows[offset:end], "offset": offset, "page_contract": tooloutput.PageContract{Collection: "groups", Revision: revision}}
	if end < len(rows) {
		out["next_offset"] = end
		out["truncated"] = true
	}
	raw, err := surveyjson.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("encode inventory accounting: %w", err)
	}
	return string(raw), nil
}

func inventoryInt(args map[string]any, key string, fallback int) int {
	switch n := args[key].(type) {
	case float64:
		return int(n)
	case int:
		return n
	case json.Number:
		if value, err := n.Int64(); err == nil {
			return int(value)
		}
	}
	return fallback
}

// candidateInventory never contributes to accepted accounting. Only a decoded,
// structurally valid candidate can supply the separately labeled draft view.
func (m InventoryAccounting) candidateInventory(ctx context.Context, run *api.WorkflowRun, manifest workflowdef.Manifest, inventory RunInventory) (scanfindings.InventoryAccount, string, error) {
	empty := scanfindings.AccountInventory(inventory.Groups, nil, nil)
	vars, err := m.Store.GetScaffoldVars(ctx, run.ID)
	if err != nil {
		return empty, "", err
	}
	repair, err := CurrentReviewRepair(vars, run.CurrentPhase)
	if err != nil {
		return empty, "", err
	}
	if repair == nil || repair.CandidateMessageID == "" || m.Sessions == nil {
		return empty, "absent", nil
	}
	phase, ok := manifest.PhaseByID(run.CurrentPhase)
	if !ok || phase.ReviewLoop == nil {
		return empty, "absent", nil
	}
	messages, err := m.Sessions.GetMessages(ctx, run.SessionID)
	if err != nil {
		return empty, "", err
	}
	for _, msg := range messages {
		if msg.ID != repair.CandidateMessageID || msg.ToolResult == nil {
			continue
		}
		account, err := m.candidateAccount(ctx, run, *phase.ReviewLoop, msg.ToolResult.ToolArgs, inventory.Groups)
		if errors.Is(err, errCandidateInvalid) {
			// A malformed candidate is a labeled draft state, not a read failure.
			return empty, "invalid", nil
		}
		if err != nil {
			return empty, "", err
		}
		return account, "draft", nil
	}
	return empty, "unavailable", nil
}

// errCandidateInvalid marks a defect in the model-authored candidate verdict.
var errCandidateInvalid = errors.New("candidate verdict invalid")

// candidateAccount accounts the inventory against a candidate's verdict
// arguments. Defects in those arguments wrap errCandidateInvalid; any other
// error is a host read failure.
func (m InventoryAccounting) candidateAccount(ctx context.Context, run *api.WorkflowRun, def workflowdef.ReviewLoopDef, args map[string]any, groups []scanfindings.InventoryGroup) (scanfindings.InventoryAccount, error) {
	var none scanfindings.InventoryAccount
	verdict, _, _, err := parseSubmitVerdictArgs(def, args)
	if err != nil {
		return none, fmt.Errorf("%w: %w", errCandidateInvalid, err)
	}
	rules, err := m.VerdictRulesFor(ctx, run)
	if err != nil {
		return none, err
	}
	if err := ValidateReviewLoopVerdict(def, verdict, rules); err != nil {
		return none, fmt.Errorf("%w: %w", errCandidateInvalid, err)
	}
	sets, err := ParseVerdictSetAsides(def, verdict)
	if err != nil {
		return none, fmt.Errorf("%w: %w", errCandidateInvalid, err)
	}
	byField, err := ParseVerdictClaims(def, verdict)
	if err != nil {
		return none, fmt.Errorf("%w: %w", errCandidateInvalid, err)
	}
	var linked []string
	for _, claims := range byField {
		for _, claim := range claims {
			linked = append(linked, claim.ScanGroupIDs...)
		}
	}
	account := scanfindings.AccountInventory(groups, linked, setAsideSelectors(sets))
	if len(account.Unknown) > 0 {
		return none, fmt.Errorf("%w: unknown scan groups %v", errCandidateInvalid, account.Unknown)
	}
	return account, nil
}

func selectorPartlyMatches(selector scanfindings.SetAside, group scanfindings.InventoryGroup) bool {
	if !selector.HasSelector() || selector.Selects(group) {
		return false
	}
	for _, path := range group.Paths {
		one := group
		one.Paths = []string{path}
		if selector.Selects(one) {
			return true
		}
	}
	return false
}
