package detectionpack

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// panicLedger is process-lifetime and outlives any one Matcher, which is
// rebuilt on every catalog reload.
var panicLedger = struct {
	mu      sync.Mutex
	skipped map[string]string // packID+"\x00"+ruleID -> recover() rendering
}{skipped: map[string]string{}}

// recordSkippedRulePanic notes that ruleID in packID panicked during
// evaluation and was skipped for the rest of this run.
func recordSkippedRulePanic(packID, ruleID string, recovered any) {
	panicLedger.mu.Lock()
	defer panicLedger.mu.Unlock()
	panicLedger.skipped[packID+"\x00"+ruleID] = fmt.Sprint(recovered)
}

// SkippedRuleWarnings renders rules in packID that panicked during evaluation
// and were skipped for the rest of this run, in the shape Settings renders
// Pack.LoadWarnings.
func SkippedRuleWarnings(packID string) []string {
	panicLedger.mu.Lock()
	defer panicLedger.mu.Unlock()
	prefix := packID + "\x00"
	var out []string
	for key, recovered := range panicLedger.skipped {
		ruleID, ok := strings.CutPrefix(key, prefix)
		if !ok {
			continue
		}
		out = append(out, fmt.Sprintf(
			"rule %s panicked during evaluation (%s) and was skipped for the rest of this run",
			ruleID, recovered))
	}
	sort.Strings(out)
	return out
}
