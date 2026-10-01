package gotreesitter

// defaultQueryMatchWorkBudget bounds enumeration steps per (pattern,node)
// match attempt. 2^20 is far above any legitimate O(n^2) success enumeration
// on <64-child nodes, and aborts a pathological 2^n walk in single-digit ms.
const defaultQueryMatchWorkBudget = 1_000_000

// queryMatchBudget bounds the query matcher's combination enumeration for a
// single (pattern,node) attempt. A nil *queryMatchBudget means "unbounded"
// (legacy behavior) so callers that opt out keep exact current behavior.
type queryMatchBudget struct {
	remaining int
	trip      bool
}

// newQueryMatchBudget returns a budget with the given step limit, or nil
// (unbounded) when limit <= 0.
func newQueryMatchBudget(limit int) *queryMatchBudget {
	if limit <= 0 {
		return nil
	}
	return &queryMatchBudget{remaining: limit}
}

// charge consumes one enumeration step. It returns true while budget remains
// and false once exhausted (latching trip). A nil budget always returns true.
func (b *queryMatchBudget) charge() bool {
	if b == nil {
		return true
	}
	if b.remaining <= 0 {
		b.trip = true
		return false
	}
	b.remaining--
	return true
}

// tripped reports whether this budget was exhausted during matching.
func (b *queryMatchBudget) tripped() bool {
	return b != nil && b.trip
}
