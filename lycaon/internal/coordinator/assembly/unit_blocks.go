package assembly

import "github.com/lycaon/lycaon/pkg/api"

// omittedUnits returns the instruction units the turn ledger left out.
func (e *promptSurface) omittedUnits(sess *api.Session) map[string]bool {
	if e == nil || sess == nil || e.deps.OmittedUnits == nil {
		return nil
	}
	return e.deps.OmittedUnits(sess.ID)
}
