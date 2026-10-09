package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"slices"

	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowpresentation "github.com/lycaon/lycaon/internal/workflow/presentation"
	workflowreview "github.com/lycaon/lycaon/internal/workflow/review"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	workflowvalidation "github.com/lycaon/lycaon/internal/workflow/validation"
	"github.com/lycaon/lycaon/pkg/api"
)

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
	run, err := m.Store.Runs.ActiveBySession(ctx, sessionID)
	if err != nil {
		return "", err
	}
	if run == nil {
		return "", &toolrejection.ToolReject{Code: workflowreview.SubmitVerdictUnavailableCode, Data: map[string]any{"reason": "no_active_workflow_run"}}
	}
	unlock := m.Vars.Lock(run.ID)
	defer unlock()
	run, err = m.Store.Runs.Get(ctx, run.ID)
	if err != nil {
		return "", err
	}
	if m.Coverage == nil || m.Coverage.Inventory == nil {
		return "", fmt.Errorf("workflow scan ledger unavailable")
	}
	manifest, err := m.Resolver.ForRun(ctx, run)
	if err != nil {
		return "", err
	}
	inventory, err := workflowreview.LoadRunInventory(ctx, m.Coverage.Inventory, run.ID)
	if err != nil {
		return "", err
	}
	if !inventory.Settled {
		return "", &toolrejection.ToolReject{Code: workflowreview.SubmitVerdictScansPendingCode}
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
	verdicts, err := workflowpresentation.ReviewVerdicts(ctx, m.Verdicts, run, manifest)
	if err != nil {
		return "", err
	}
	var linked []string
	for _, claim := range workflowpresentation.ReconcileClaims(verdicts) {
		linked = append(linked, claim.ScanGroupIDs...)
	}
	account := scanfindings.AccountInventory(inventory.Groups, linked, workflowpresentation.RunSetAsides(verdicts))
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
func (m InventoryAccounting) candidateInventory(ctx context.Context, run *api.WorkflowRun, manifest workflowdef.Manifest, inventory workflowreview.RunInventory) (scanfindings.InventoryAccount, string, error) {
	empty := scanfindings.AccountInventory(inventory.Groups, nil, nil)
	vars, err := m.Store.Runs.GetScaffoldVars(ctx, run.ID)
	if err != nil {
		return empty, "", err
	}
	repair, err := runstate.CurrentReviewRepair(vars, run.CurrentPhase)
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
	verdict, _, _, err := workflowreview.ParseSubmitVerdictArgs(def, args)
	if err != nil {
		return none, fmt.Errorf("%w: %w", errCandidateInvalid, err)
	}
	rules, err := m.Verdicts.VerdictRulesFor(ctx, run)
	if err != nil {
		return none, err
	}
	if err := workflowvalidation.ValidateReviewLoopVerdict(def, verdict, rules); err != nil {
		return none, fmt.Errorf("%w: %w", errCandidateInvalid, err)
	}
	sets, err := workflowvalidation.ParseVerdictSetAsides(def, verdict)
	if err != nil {
		return none, fmt.Errorf("%w: %w", errCandidateInvalid, err)
	}
	byField, err := workflowvalidation.ParseVerdictClaims(def, verdict)
	if err != nil {
		return none, fmt.Errorf("%w: %w", errCandidateInvalid, err)
	}
	var linked []string
	for _, claims := range byField {
		for _, claim := range claims {
			linked = append(linked, claim.ScanGroupIDs...)
		}
	}
	account := scanfindings.AccountInventory(groups, linked, workflowpresentation.SetAsideSelectors(sets))
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
