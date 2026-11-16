package confine

import (
	"os"
	"sort"
)

// LineageMarkerForTest returns the descendant marker a confined launch hands its
// child, so a test can start a descendant without a full spawn.
func LineageMarkerForTest(c *Confinement) *os.File {
	if c == nil {
		return nil
	}
	return c.lineage.ChildFile()
}

// OrphanEgressAttempts returns the recorded refusals, newest activity first.
func OrphanEgressAttempts() []OrphanEgressAttempt {
	orphanEgress.mu.Lock()
	out := make([]OrphanEgressAttempt, 0, len(orphanEgress.attempts))
	for _, entry := range orphanEgress.attempts {
		out = append(out, *entry)
	}
	orphanEgress.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].LastAt.After(out[j].LastAt) })
	return out
}

// ForgetOrphanEgressAttempts clears the recorded refusals.
func ForgetOrphanEgressAttempts() {
	orphanEgress.mu.Lock()
	orphanEgress.attempts = map[string]*OrphanEgressAttempt{}
	orphanEgress.mu.Unlock()
}
