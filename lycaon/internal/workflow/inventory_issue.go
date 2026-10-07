package workflow

import (
	"slices"
	"sync"

	"github.com/lycaon/lycaon/internal/guidance"
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
)

// InventoryIssue is a verdict's scanner-inventory shortfall with the facts a
// corrected resubmission needs.
type InventoryIssue struct {
	guidance.ReportDocumentIssue
	// Unaccounted are the settled groups no claim links and no set-aside selects.
	Unaccounted []scanfindings.InventoryGroup
	// Regressed are groups this phase's previous submission had accounted for.
	Regressed []string
}

// Bounds on the refusal's structured facts: ids stay complete for any inventory
// a review can hold; full rows carry locations for the first groups.
const (
	inventoryIDLimit  = 64
	inventoryRowLimit = 24
)

// Details are the refusal facts beside the bounded offender sample.
func (i *InventoryIssue) Details() map[string]any {
	details := guidance.OffenderHintData(i.Offenders)
	details["reason"] = i.Reason
	details["offender_count"] = i.Count
	details["offenders_omitted"] = max(0, i.Count-len(i.Offenders))
	ids := make([]string, 0, min(len(i.Unaccounted), inventoryIDLimit))
	rows := make([]map[string]any, 0, min(len(i.Unaccounted), inventoryRowLimit))
	for n, g := range i.Unaccounted {
		if n < inventoryIDLimit {
			ids = append(ids, g.ID)
		}
		if n < inventoryRowLimit {
			rows = append(rows, map[string]any{
				"id": g.ID, "scanner": g.Scanner, "level": string(g.Level), "rule_id": g.RuleID,
				"paths": append([]string(nil), g.Paths...),
			})
		}
	}
	details["unaccounted_group_ids"] = ids
	details["unaccounted_omitted"] = max(0, len(i.Unaccounted)-inventoryIDLimit)
	details["unaccounted_groups"] = rows
	details["regressed_group_ids"] = append([]string(nil), i.Regressed...)
	return details
}

// inventoryShortfalls remembers each phase's last unaccounted ids so the next
// refusal can name what a resubmission lost.
type inventoryShortfalls struct {
	mu   sync.Mutex
	last map[string][]string
}

// noteInventoryShortfall records the phase's unaccounted ids and returns those
// the previous submission had accounted for.
func (m *RunManager) noteInventoryShortfall(runID, phase string, unaccounted []scanfindings.InventoryGroup) []string {
	ids := make([]string, 0, len(unaccounted))
	for _, g := range unaccounted {
		ids = append(ids, g.ID)
	}
	m.inventoryShortfalls.mu.Lock()
	defer m.inventoryShortfalls.mu.Unlock()
	if m.inventoryShortfalls.last == nil {
		m.inventoryShortfalls.last = map[string][]string{}
	}
	key := runID + "\x00" + phase
	previous, seen := m.inventoryShortfalls.last[key]
	m.inventoryShortfalls.last[key] = ids
	if !seen {
		return nil
	}
	var regressed []string
	for _, id := range ids {
		if !slices.Contains(previous, id) {
			regressed = append(regressed, id)
		}
	}
	return regressed
}
