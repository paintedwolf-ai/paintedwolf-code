package syntax

import (
	"fmt"
	"math"
)

// AllocationBudget is one logical preallocation ledger shared by parsing,
// static classification, and a caller's lowering. It counts old backing arrays
// until the unit is discarded, independent of garbage collector timing.
// It is invocation-local and must not be shared concurrently.
type AllocationBudget struct {
	maximum int64
	used    int64
}

func NewAllocationBudget(maximum int64) (*AllocationBudget, error) {
	if maximum <= 0 {
		return nil, fmt.Errorf("allocation budget must be positive")
	}
	return &AllocationBudget{maximum: maximum}, nil
}

func (b *AllocationBudget) UsedBytes() int64    { return b.used }
func (b *AllocationBudget) MaximumBytes() int64 { return b.maximum }

// ReserveBytes admits storage before allocation. Refusal leaves the ledger
// unchanged, so a caller cannot hide an earlier unit's retained allocations.
func (b *AllocationBudget) ReserveBytes(bytes int64, pos Pos) error {
	observed := int64(math.MaxInt64)
	if bytes >= 0 && b.used <= math.MaxInt64-bytes {
		observed = b.used + bytes
	}
	if observed > b.maximum {
		return &BudgetError{"parser_scratch_bytes", b.maximum, observed, pos}
	}
	b.used = observed
	return nil
}

// ReserveHeap includes rounded payload and allocation bookkeeping, matching
// the selected parser's accounting. It also bounds pre-GC allocation churn.
func (b *AllocationBudget) ReserveHeap(bytes int64, pos Pos) error {
	if bytes < 0 || bytes > math.MaxInt64-8256 {
		return b.ReserveBytes(math.MaxInt64, pos)
	}
	rounded := int64(16)
	if bytes <= 32768 {
		for rounded < bytes {
			rounded *= 2
		}
	} else {
		rounded = (bytes + 8191) / 8192 * 8192
	}
	return b.ReserveBytes(rounded+64, pos)
}

func (b *AllocationBudget) releaseFrame(bytes int64) { b.used -= bytes }
