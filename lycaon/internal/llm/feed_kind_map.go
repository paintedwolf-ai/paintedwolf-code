package llm

import (
	"github.com/lycaon/lycaon/internal/modelfeed"
)

// CatalogAuthoritative reports whether assignable models should use the
// catalog-first merge (mapped kind ∧ usable modelfeed document).
func CatalogAuthoritative(kind string, feedUsable bool) bool {
	switch kind {
	case "azure", "vertex", "vertex-express":
		// Feed rows provide metadata and pricing for these transports, but their
		// assignable ids must come from deployments or typed live discovery.
		return false
	}
	_, mapped := modelfeed.FeedKeyForKind(kind)
	return mapped && feedUsable
}
