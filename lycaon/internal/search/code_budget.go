package search

import (
	"strings"
	"time"
)

// SearchBudget bounds time and results for one search request.
type SearchBudget string

const (
	// BudgetComplete uses the full result and time limits.
	BudgetComplete SearchBudget = "complete"
	// BudgetInteractive prioritizes origin roots with smaller limits.
	BudgetInteractive SearchBudget = "interactive"
)

const (
	// Interactive limits leave room for local result shaping.
	InteractiveCodeMaxHits  = 16
	InteractiveFileMaxHits  = 8
	InteractiveStoreMaxHits = 32
	// InteractiveSymbolMaxHits bounds declarations per project.
	InteractiveSymbolMaxHits = 16
)

// ParseSearchBudget maps a wire value. Empty is complete. Unknown is invalid.
func ParseSearchBudget(raw string) (SearchBudget, bool) {
	switch strings.TrimSpace(strings.ToLower(raw)) {
	case "", string(BudgetComplete):
		return BudgetComplete, true
	case string(BudgetInteractive):
		return BudgetInteractive, true
	default:
		return "", false
	}
}

func (b SearchBudget) normalize() SearchBudget {
	if b == BudgetInteractive {
		return BudgetInteractive
	}
	return BudgetComplete
}

func (b SearchBudget) codeCaps() (lines, files int) {
	if b.normalize() == BudgetInteractive {
		return InteractiveCodeMaxHits + 1, InteractiveFileMaxHits + 1
	}
	return SearchExecutorProbeHits, SearchExecutorProbeHits
}

// Both budgets retain partial results when the time limit expires.
const (
	interactiveCodeWallBudget = 2 * time.Second
	completeCodeWallBudget    = 10 * time.Second
)

// Wall is how long a live source leg may run before it answers with what it has.
func (b SearchBudget) Wall() time.Duration {
	if b.normalize() == BudgetInteractive {
		return interactiveCodeWallBudget
	}
	return completeCodeWallBudget
}

func (b SearchBudget) symbolCap() int {
	if b.normalize() == BudgetInteractive {
		return InteractiveSymbolMaxHits
	}
	return SymbolLegCap
}

func (b SearchBudget) storeCap() int {
	if b.normalize() == BudgetInteractive {
		return InteractiveStoreMaxHits + 1
	}
	return SearchExecutorProbeHits
}

const (
	// interactiveStorePostScanRows keeps typeahead post-filtering cheap;
	// completeStorePostScanRows lets the full stage search deep.
	interactiveStorePostScanRows = 4_000
	completeStorePostScanRows    = 50_000
)

func (b SearchBudget) storePostScanCap() int {
	if b.normalize() == BudgetInteractive {
		return interactiveStorePostScanRows
	}
	return completeStorePostScanRows
}

// orderCodeRoots puts origin roots first. Other roots stay in the list.
func orderCodeRoots(roots []CodeRoot, originProjectID string) []CodeRoot {
	origin := strings.TrimSpace(originProjectID)
	if origin == "" || len(roots) < 2 {
		return append([]CodeRoot(nil), roots...)
	}
	first := make([]CodeRoot, 0, len(roots))
	rest := make([]CodeRoot, 0, len(roots))
	for _, root := range roots {
		if strings.TrimSpace(root.ProjectID) == origin {
			first = append(first, root)
		} else {
			rest = append(rest, root)
		}
	}
	return append(first, rest...)
}
