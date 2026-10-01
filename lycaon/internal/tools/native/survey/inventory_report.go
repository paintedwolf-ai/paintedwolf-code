package survey

import (
	"context"
	"sync"
)

// inventoryFacts states that a result was computed from the last complete
// source generation while a newer one builds.
type inventoryFacts struct {
	Fresh    bool   `json:"fresh"`
	Revision uint64 `json:"revision,omitempty"`
}

// inventoryReport collects the freshness of every inventory one call read.
type inventoryReport struct {
	mu       sync.Mutex
	stale    bool
	revision uint64
}

type inventoryReportKey struct{}

func withInventoryReport(ctx context.Context) (context.Context, *inventoryReport) {
	report := &inventoryReport{}
	return context.WithValue(ctx, inventoryReportKey{}, report), report
}

func reportInventory(ctx context.Context, inventory sourceInventory) {
	report, ok := ctx.Value(inventoryReportKey{}).(*inventoryReport)
	if !ok || !inventory.stale {
		return
	}
	report.mu.Lock()
	defer report.mu.Unlock()
	report.stale = true
	if report.revision == 0 || inventory.revision < report.revision {
		report.revision = inventory.revision
	}
}

// facts is nil unless a stale generation contributed to the result.
func (r *inventoryReport) facts() *inventoryFacts {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.stale {
		return nil
	}
	return &inventoryFacts{Revision: r.revision}
}

// inventoryFactsFrom reads the report a call installed on ctx.
func inventoryFactsFrom(ctx context.Context) *inventoryFacts {
	report, _ := ctx.Value(inventoryReportKey{}).(*inventoryReport)
	return report.facts()
}
