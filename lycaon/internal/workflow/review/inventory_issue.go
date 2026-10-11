package review

import (
	"slices"
	"sync"

	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
)

// inventoryShortfalls remembers each phase's last unaccounted ids so the next
// refusal can name what a resubmission lost.
type inventoryShortfalls struct {
	mu   sync.Mutex
	last map[string][]string
}

// noteInventoryShortfall records the phase's unaccounted ids and returns those
// the previous submission had accounted for.
func (m *Coverage) noteInventoryShortfall(runID, phase string, unaccounted []scanfindings.InventoryGroup) []string {
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
