package oar

import (
	"github.com/lycaon/lycaon/internal/coordinator/surfacecatalog"
)

// factValues closes the literal vocabulary of string facts; facts absent here
// accept any literal.
var factValues = map[string]map[string]struct{}{
	"batch_phase":   setOf("pre_dispatch", "dispatch", "integrate", "synthesize", "closed"),
	"phase":         setOf("plan", "execute", "verify", "report", "completed", "review", "drafting", "building", "approved", "changed", "ready", "rejected"),
	"surface":       surfaceFactVocabulary(),
	"scope_mode":    setOf("read", "write"),
	"status":        setOf("queued", "running", "completed", "failed", "canceled", "aborted"),
	"promote_order": setOf("independent", "clean_if_first", "sequential", "clean_after"),
	"profile":       setOf("coordinator", "worker", "implementer", "reviewer"),
}

func surfaceFactVocabulary() map[string]struct{} {
	catalog, err := surfacecatalog.Load()
	if err != nil {
		return nil
	}
	ids := catalog.SurfaceIDs()
	out := make(map[string]struct{}, len(ids)+1)
	for _, id := range ids {
		out[id] = struct{}{}
	}
	out[surfacecatalog.NoFolderAllowlistID] = struct{}{}
	return out
}

const rejectionCodeFact = "rejection_code"

func setOf(vals ...string) map[string]struct{} {
	m := make(map[string]struct{}, len(vals))
	for _, v := range vals {
		m[v] = struct{}{}
	}
	return m
}
