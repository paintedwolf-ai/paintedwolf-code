package validation

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/rules"
	"github.com/lycaon/lycaon/internal/vocabulary"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflowdiag"
	"github.com/lycaon/lycaon/pkg/api"
)

// ValidateBundled reports every unregistered workflow and rule predicate.
func ValidateBundled(reg *conditions.ConditionRegistry, manifests *workflowdef.Registry, ruleConfigs []*rules.RulesConfig) []api.ComposeValidationError {
	if reg == nil {
		return []api.ComposeValidationError{workflowdiag.EmitDefault(workflowdiag.MustCode("load_error"), "registry",
			map[string]any{"detail": "nil condition registry"})}
	}
	var out []api.ComposeValidationError
	if manifests != nil {
		for key, m := range manifests.All() {
			out = append(out, validateManifest(reg, key, m)...)
		}
	}
	for i, cfg := range ruleConfigs {
		if cfg == nil {
			continue
		}
		out = append(out, validateRules(reg, i, cfg)...)
	}
	return out
}

func validateManifest(reg *conditions.ConditionRegistry, key string, m workflowdef.Manifest) []api.ComposeValidationError {
	var out []api.ComposeValidationError
	for _, phase := range m.PhaseDefs {
		out = append(out, validatePhasePredicates(reg, fmt.Sprintf("workflow[%s].phases[%s]", key, phase.ID), phase)...)
	}
	return out
}

func validatePhasePredicates(reg *conditions.ConditionRegistry, path string, phase workflowdef.PhaseDef) []api.ComposeValidationError {
	var out []api.ComposeValidationError
	for _, field := range phase.PredicateFields() {
		name := path + "." + field.Path
		if field.Err != nil {
			out = append(out, workflowdiag.EmitDefault(workflowdiag.MustCode("boolexpr_parse"), name, map[string]any{"detail": field.Err.Error()}))
			continue
		}
		for _, leaf := range field.Leaves {
			out = append(out, validateLeafID(reg, name, leaf)...)
		}
	}
	return out
}

func validateLeafID(reg *conditions.ConditionRegistry, field, id string) []api.ComposeValidationError {
	id = strings.TrimSpace(id)
	if conditions.ForbiddenPredicateConfigError(id) != nil {
		hint := conditions.ForbiddenPredicateReplacementHint(id)
		return []api.ComposeValidationError{workflowdiag.EmitDefault(workflowdiag.MustCode("forbidden_predicate"), field,
			map[string]any{"leaf": id, "replacement_hint": hint})}
	}
	if vocabulary.IsCatalogOnly(id) {
		return []api.ComposeValidationError{workflowdiag.EmitDefault(workflowdiag.MustCode("catalog_only_predicate"), field,
			map[string]any{"leaf": id})}
	}
	if !reg.Has(id) || !vocabulary.IsPredicate(id) {
		return []api.ComposeValidationError{workflowdiag.EmitDefault(workflowdiag.MustCode("unknown_predicate"), field,
			map[string]any{"leaf": id})}
	}
	return nil
}

func validateWhenKey(field, key string) []api.ComposeValidationError {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil
	}
	if conditions.IsForbidden(key) {
		hint := conditions.ForbiddenPredicateReplacementHint(key)
		return []api.ComposeValidationError{workflowdiag.EmitDefault(workflowdiag.MustCode("forbidden_predicate"), field,
			map[string]any{"leaf": key, "replacement_hint": hint})}
	}
	if conditions.IsHostOnlyCondition(key) {
		return []api.ComposeValidationError{workflowdiag.EmitDefault(workflowdiag.MustCode("forbidden_predicate"), field,
			map[string]any{"leaf": key, "replacement_hint": "host-only conditions cannot appear in rule when"})}
	}
	return nil
}

func validateRules(reg *conditions.ConditionRegistry, idx int, cfg *rules.RulesConfig) []api.ComposeValidationError {
	var out []api.ComposeValidationError
	for _, rule := range cfg.Rules {
		for key := range rule.When {
			out = append(out, validateWhenKey(fmt.Sprintf("rules[%d].%s.when", idx, rule.ID), key)...)
		}
		field := fmt.Sprintf("rules[%d].%s.when", idx, rule.ID)
		// The map form is validated from its leaves, whose names are registry keys,
		// not from re-lexed rendered text.
		leaves, err := rules.CanonicalWhenLeaves(rule.When)
		if err != nil {
			out = append(out, workflowdiag.EmitDefault(workflowdiag.MustCode("boolexpr_parse"), field,
				map[string]any{"detail": err.Error()}))
			continue
		}
		for _, leaf := range leaves {
			out = append(out, validateLeafID(reg, field, leaf.Name)...)
		}
	}
	return out
}
