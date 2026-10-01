// Package recall answers questions about the past from the search projection.
package recall

// Resolution is the closed vocabulary for what a recall answered. Truncation is
// not a value here — a truncated answer still matched, and rides
// Count.Relation. Nor is scope denial, which is a structured reject.
type Resolution string

const (
	ResolutionMatched Resolution = "matched"
	// ResolutionNoMatchInScope — the scope holds indexed rows; none matched.
	ResolutionNoMatchInScope Resolution = "no_match_in_scope"
	// ResolutionScopeEmpty — the scope holds no indexed rows at all. Distinct
	// from no_match_in_scope: it separates "the leg did not observe that" from
	// "nothing from that leg was ever indexed."
	ResolutionScopeEmpty Resolution = "scope_empty"
	// ResolutionRecordDeleted — only tombstones matched. The record existed.
	ResolutionRecordDeleted Resolution = "record_deleted"
	// ResolutionExecutorDegraded — an executor failed. Not evidence of absence.
	ResolutionExecutorDegraded Resolution = "executor_degraded"
)

// Currency states how a path-anchored record compares to the tree now. A
// recalled observation is a fact about the past.
type Currency string

const (
	// CurrencyUnchanged — no write to that path is recorded after the
	// observation. Not a claim that the bytes are identical.
	CurrencyUnchanged Currency = "unchanged"
	CurrencyChanged   Currency = "changed"
	CurrencyPathGone  Currency = "path_gone"
	// CurrencyUnknown — no path, or the ledger could not answer.
	CurrencyUnknown Currency = "unknown"
)
