package workflow

import (
	workflowvalidation "github.com/lycaon/lycaon/internal/workflow/validation"
)

// ObligationSpecsFromKinds extracts validators from wired kinds.
func ObligationSpecsFromKinds(kinds map[string]ObligationKind) workflowvalidation.ObligationSpecs {
	if len(kinds) == 0 {
		return nil
	}
	out := make(workflowvalidation.ObligationSpecs, len(kinds))
	for name, kind := range kinds {
		out[name] = kind
	}
	return out
}
