package workflowdiag

import (
	"github.com/lycaon/lycaon/internal/vocabulary"
	"github.com/lycaon/lycaon/pkg/api"
	"strings"
)

func PredicateLeaf(field, id string, allowed func(string) bool, overlay, readiness bool) []api.ComposeValidationError {
	id = strings.TrimSpace(id)
	if vocabulary.IsForbidden(id) {
		hint := vocabulary.ForbiddenPredicateReplacementHint(id)
		return []api.ComposeValidationError{EmitDefault(MustCode("forbidden_predicate"), field,
			map[string]any{"leaf": id, "replacement_hint": hint})}
	}
	if vocabulary.IsHostOnlyCondition(id){return []api.ComposeValidationError{EmitDefault(MustCode("forbidden_predicate"),field,map[string]any{"leaf":id,"replacement_hint":"use a readable condition"})}}
 if vocabulary.IsCatalogOnly(id) {
		return []api.ComposeValidationError{EmitDefault(MustCode("catalog_only_predicate"), field,
			map[string]any{"leaf": id})}
	}
	if !allowed(id) {
		code := "unknown_predicate"
		if readiness {
			code = "unknown_readiness"
		}
		return []api.ComposeValidationError{EmitDefault(MustCode(code), field,
			map[string]any{"leaf": id})}
	}
	if overlay && !vocabulary.IsOverlayLeaf(id) {
		return []api.ComposeValidationError{EmitDefault(MustCode("domain_leaf_on_overlay"), field, map[string]any{"leaf": id})}
	}
	return nil
}
