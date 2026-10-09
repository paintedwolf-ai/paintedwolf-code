package review

import (
	"context"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	runstate "github.com/lycaon/lycaon/internal/workflow/runstate"
	"sort"
	"strings"
)

// StampedReviewVerdict is one terminal review_loop verdict the run has recorded.
type StampedReviewVerdict struct {
	// Phase is the manifest phase that stamped it.
	Phase string
	// EvidenceKey is the review_loop key the verdict satisfied.
	EvidenceKey string
	// Fields are the verdict members in render order. A `claims`-typed member
	// carries its JSON array verbatim.
	Fields []VerdictField
}

// VerdictField is one member of a stamped verdict.
type VerdictField struct {
	Name  string
	Value string
}

// StampedReviewVerdicts returns every terminal review_loop verdict the session's
// active run has recorded, in manifest phase order. An evidence_key names host
// state a leg has no tool to read, so the record travels with the assignment.
// Empty until the first verdict is stamped.
func (m *Verdicts) StampedReviewVerdicts(ctx context.Context, sessionID string) []StampedReviewVerdict {
	if m == nil || strings.TrimSpace(sessionID) == "" {
		return nil
	}
	active, err := m.Runs.ActiveBySession(ctx, sessionID)
	if err != nil || active == nil {
		return nil
	}
	manifest, err := m.Resolver.ForRun(ctx, active)
	if err != nil {
		return nil
	}
	vars, err := m.Runs.GetScaffoldVars(ctx, active.ID)
	if err != nil {
		return nil
	}
	var out []StampedReviewVerdict
	for _, phase := range manifest.PhaseDefs {
		if phase.ReviewLoop == nil {
			continue
		}
		key := strings.TrimSpace(phase.ReviewLoop.EvidenceKey)
		if key == "" {
			continue
		}
		verdict := runstate.ReviewVerdictFromVars(vars, key)
		if len(verdict) == 0 {
			continue
		}
		out = append(out, StampedReviewVerdict{
			Phase:       phase.ID,
			EvidenceKey: key,
			Fields:      OrderVerdictFields(verdict),
		})
	}
	return out
}

// OrderVerdictFields renders the terminal decision first, then the rest
// alphabetically. Members arrive as an unordered map, so without this the same
// record reads differently in a leg assignment and in the run's report.
func OrderVerdictFields(verdict map[string]string) []VerdictField {
	names := make([]string, 0, len(verdict))
	for name := range verdict {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]VerdictField, 0, len(names))
	if value, ok := verdict[workflowdef.VerdictDecisionKey]; ok {
		out = append(out, VerdictField{Name: workflowdef.VerdictDecisionKey, Value: value})
	}
	for _, name := range names {
		if name == workflowdef.VerdictDecisionKey {
			continue
		}
		out = append(out, VerdictField{Name: name, Value: verdict[name]})
	}
	return out
}
