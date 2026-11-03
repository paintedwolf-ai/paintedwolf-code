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
	maximum  int64
	used     int64
	admitted int64
	admit    func(int64) error
}

// admissionChunk is the granularity at which an admitted ledger charges its
// enclosing aggregate. The aggregate holds the ledger's high-water mark rounded
// up to this size; frame releases stay charged until the caller discards it.
const admissionChunk int64 = 64 << 10

func NewAllocationBudget(maximum int64) (*AllocationBudget, error) {
	if maximum <= 0 || maximum > math.MaxInt64-8256 {
		return nil, fmt.Errorf("allocation budget must be positive")
	}
	return &AllocationBudget{maximum: maximum}, nil
}

// NewAdmittedAllocationBudget charges growth to an enclosing aggregate before
// the ledger admits it, so the aggregate never holds unused headroom. The caller
// releases everything admit accepted once it discards the ledger.
func NewAdmittedAllocationBudget(maximum int64, admit func(int64) error) (*AllocationBudget, error) {
	if admit == nil {
		return nil, fmt.Errorf("admitted allocation budget requires an aggregate")
	}
	b, err := NewAllocationBudget(maximum)
	if err != nil {
		return nil, err
	}
	b.admit = admit
	return b, nil
}

func (b *AllocationBudget) UsedBytes() int64    { return b.used }
func (b *AllocationBudget) MaximumBytes() int64 { return b.maximum }

// AdmittedBytes reports what the enclosing aggregate currently holds for this ledger.
func (b *AllocationBudget) AdmittedBytes() int64 { return b.admitted }

// ReserveBytes admits storage before allocation. Refusal leaves the ledger
// unchanged, so a caller cannot hide an earlier unit's retained allocations.
func (b *AllocationBudget) ReserveBytes(bytes int64, pos Pos) error {
	observed := int64(math.MaxInt64)
	if bytes >= 0 && b.used <= math.MaxInt64-bytes {
		observed = b.used + bytes
	}
	if observed > b.maximum {
		return &BudgetError{Kind: "parser_scratch_bytes", Maximum: b.maximum, Observed: observed, Pos: pos}
	}
	if b.admit != nil && observed > b.admitted {
		grow := min(max(observed-b.admitted, admissionChunk), b.maximum-b.admitted)
		if err := b.admit(grow); err != nil {
			return &BudgetError{Kind: "aggregate_admission", Maximum: b.maximum, Observed: observed, Pos: pos, Cause: err}
		}
		b.admitted += grow
	}
	b.used = observed
	return nil
}

// ReserveHeap includes rounded payload and allocation bookkeeping, matching
// the selected parser's accounting. It also bounds pre-GC allocation churn.
func (b *AllocationBudget) ReserveHeap(bytes int64, pos Pos) error {
	if bytes < 0 || bytes > math.MaxInt64-8256 {
		return &BudgetError{Kind: "parser_scratch_bytes", Maximum: b.maximum, Observed: math.MaxInt64, Pos: pos}
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
