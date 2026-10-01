package oar

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/oarcore"
)

// factValues closes the literal vocabulary of string facts; facts absent here
// accept any literal.
var factValues = map[string]map[string]struct{}{
	"batch_phase": setOf("pre_dispatch", "dispatch", "integrate", "synthesize", "closed"),
	"phase":       setOf("plan", "execute", "verify", "report", "completed", "review", "drafting", "building", "approved", "changed", "ready", "rejected"),
	"surface": setOf(
		"implement_routing", "implement_dispatch", "implement_synthesis",
		"orchestrate_plan", "recon_reconcile", "implement_overlay_promote",
		"implement_investigate", "implement_park", "workflow_compose",
		"plan_stub", "plan_research", "plan_approve", "plan_review",
		"survey_execute", "review_adjudicate", "decision_adjudicate",
		"await_user", "plan_execute", "observe_investigate", "observe_plan",
		"observe_synthesis", "no_folder_allowlist",
	),
	"scope_mode":    setOf("read", "write"),
	"status":        setOf("queued", "running", "completed", "failed", "canceled", "aborted"),
	"promote_order": setOf("independent", "clean_if_first", "sequential", "clean_after"),
	"profile":       setOf("coordinator", "worker", "implementer", "reviewer"),
}

const rejectionCodeFact = "rejection_code"

func setOf(vals ...string) map[string]struct{} {
	m := make(map[string]struct{}, len(vals))
	for _, v := range vals {
		m[v] = struct{}{}
	}
	return m
}

// ValidateConditionFactValues checks == and in literals in a when clause
// against the declared fact vocabularies. A non-empty rejectionCodes closes
// the rejection_code vocabulary.
func ValidateConditionFactValues(source string, rejectionCodes map[string]struct{}) error {
	cmps, err := oarcore.ConditionFactComparisons(source)
	if err != nil {
		return err
	}
	for _, c := range cmps {
		name := strings.TrimPrefix(strings.TrimSpace(c.Fact), "paintedwolf.")
		allowed, ok := factValues[name]
		if name == rejectionCodeFact && len(rejectionCodes) > 0 {
			allowed, ok = rejectionCodes, true
		}
		if !ok {
			continue
		}
		if _, valid := allowed[c.Value]; !valid {
			return fmt.Errorf("fact %q %s %q: value is outside the declared vocabulary", c.Fact, c.Op, c.Value)
		}
	}
	return nil
}
