package validation

import (
	"errors"
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
		out = append(out, phase.PredicateDiagnostics(fmt.Sprintf("workflow[%s].phases[%s]", key, phase.ID), reg.Has, false)...)
	}
	return out
}

func validateWhenKey(field, key string) []api.ComposeValidationError {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil
	}
	if vocabulary.IsForbidden(key) {
		hint := vocabulary.ForbiddenPredicateReplacementHint(key)
		return []api.ComposeValidationError{workflowdiag.EmitDefault(workflowdiag.MustCode("forbidden_predicate"), field,
			map[string]any{"leaf": key, "replacement_hint": hint})}
	}
	if vocabulary.IsHostOnlyCondition(key) {
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
		// Map values can contain expression delimiters.
		leaves, err := rules.CanonicalWhenLeaves(rule.When)
		if err != nil {
			out = append(out, workflowdiag.EmitDefault(workflowdiag.MustCode("boolexpr_parse"), field,
				map[string]any{"detail": err.Error()}))
			continue
		}
		for _, leaf := range leaves {
			out = append(out, workflowdiag.PredicateLeaf(field, leaf.Name, reg.Has, false, false)...)
		}
	}
	return out
}

// PredicateLoadErrors preserves field diagnostics across manifest loaders.
func PredicateLoadErrors(err error, prefix string) []api.ComposeValidationError {
	var issues workflowdef.PredicateErrors
	if !errors.As(err, &issues) {
		return nil
	}
	out := append([]api.ComposeValidationError(nil), issues...)
	if prefix != "" {
		for i := range out {
			out[i].Field = prefix + ": " + out[i].Field
		}
	}
	return out
}
