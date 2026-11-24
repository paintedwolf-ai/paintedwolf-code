package progress

import "strings"

// IsSynthesisReconcileOnly reports whether m only acknowledges worker results
// or transitions existing rows to done or na without changing labels or scope.
func (m Mutation) IsSynthesisReconcileOnly(current Snapshot) bool {
	if current.Missing() || m.IsEmpty() {
		return false
	}
	if len(m.Replace) > 0 || len(m.Add) > 0 || len(m.Remove) > 0 {
		return false
	}
	byID := make(map[string]Row, len(current.Rows))
	for _, r := range current.Rows {
		byID[r.ID] = r
	}
	for _, u := range m.Update {
		prev, ok := byID[u.ID]
		if !ok {
			return false
		}
		if u.Label != nil && strings.TrimSpace(*u.Label) != prev.Label {
			return false
		}
		if u.State == nil {
			return false
		}
		if *u.State != ProgressStateDone && *u.State != ProgressStateNA {
			return false
		}
	}
	return true
}
