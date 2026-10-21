package lineage

import "github.com/lycaon/lycaon/internal/osprocess"

// ProcessIdentity distinguishes a holder from a later reuse of its numeric PID.
// It is an observed process fact, never a grant or a cached lineage proof.
type ProcessIdentity struct {
	PID     int
	Started int64
}

func processIdentity(pid int) (ProcessIdentity, bool) {
	started, ok := osprocess.StartTime(pid)
	return ProcessIdentity{PID: pid, Started: started}, ok && started > 0
}

// Current requires positive kernel evidence of the same process incarnation.
func (p ProcessIdentity) Current() bool {
	current, ok := processIdentity(p.PID)
	return ok && current == p
}
