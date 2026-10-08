package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
)

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
func (m *RunManager) QueryWorkflowInventory(ctx context.Context, sessionID string, args map[string]any) (string, error) {
	for key := range args {
		switch key {
		case "scan_ids", "view", "inventory_revision", "selector", "offset", "limit", "project_dir":
		default:
			return "", &tools.ToolReject{Code: "TOOL_ARGS_INVALID", Data: map[string]any{"tool": "scan_query", "field": key, "reason": "accounting_requires_complete_inventory"}}
		}
	}
	run, err := m.Store.ActiveBySession(ctx, sessionID)
	if err != nil {
		return "", err
	}
	if run == nil {
		return "", &tools.ToolReject{Code: SubmitVerdictUnavailableCode, Data: map[string]any{"reason": "no_active_workflow_run"}}
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
		return "", &tools.ToolReject{Code: SubmitVerdictScansPendingCode}
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
		return "", &tools.ToolReject{Code: "TOOL_ARGS_INVALID", Data: map[string]any{"field": "scan_ids", "reason": "run_bound_scan_set_required"}}
	}
	revision := scanfindings.InventoryRevision(inventory.Scans)
	if expected, _ := args["inventory_revision"].(string); expected != "" && expected != revision {
		return "", &tools.ToolReject{Code: "SCAN_INVENTORY_STALE", Data: map[string]any{"inventory_revision": revision}}
	}
	verdicts, err := ReviewVerdicts(ctx, m, run, manifest)
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
