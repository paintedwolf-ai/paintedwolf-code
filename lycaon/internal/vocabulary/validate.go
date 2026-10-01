package vocabulary

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/boolexpr"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/rules"
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
	prefix := fmt.Sprintf("workflow[%s]", key)
	for _, p := range m.PhaseDefs {
		cw := strings.TrimSpace(p.CompleteWhen)
		fieldCW := fmt.Sprintf("%s.phases[%s].complete_when", prefix, p.ID)
		if cw == "" || cw == workflowdef.CompleteWhenGatesSatisfied {
			// Gates are validated separately.
		} else if strings.HasPrefix(cw, workflowdef.CompleteWhenGateSatisfied) {
			gate := strings.TrimPrefix(cw, workflowdef.CompleteWhenGateSatisfied)
			out = append(out, validateLeafID(reg, fieldCW, gate)...)
		} else {
			if conditions.IsCatalogStub(cw) {
				out = append(out, workflowdiag.EmitDefault(workflowdiag.MustCode("catalog_only_predicate"), fieldCW,
					map[string]any{"leaf": cw}))
			} else {
				out = append(out, validateExprLeaves(reg, fieldCW, cw)...)
			}
		}
		for i, g := range p.Gates {
			g = strings.TrimSpace(g)
			if g == "" {
				continue
			}
			out = append(out, validateLeafID(reg, fmt.Sprintf("%s.phases[%s].gates[%d]", prefix, p.ID, i), g)...)
		}
		if ew := strings.TrimSpace(p.EntryWhen); ew != "" {
			fieldEW := fmt.Sprintf("%s.phases[%s].entry_when", prefix, p.ID)
			if conditions.IsCatalogStub(ew) {
				out = append(out, workflowdiag.EmitDefault(workflowdiag.MustCode("catalog_only_predicate"), fieldEW,
					map[string]any{"leaf": ew}))
			} else {
				out = append(out, validateExprLeaves(reg, fieldEW, ew)...)
			}
		}
	}
	return out
}

func validateLeafID(reg *conditions.ConditionRegistry, field, id string) []api.ComposeValidationError {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	if conditions.IsForbidden(id) {
		hint := conditions.ForbiddenPredicateReplacementHint(id)
		return []api.ComposeValidationError{workflowdiag.EmitDefault(workflowdiag.MustCode("forbidden_predicate"), field,
			map[string]any{"leaf": id, "replacement_hint": hint})}
	}
	if conditions.IsCatalogStub(id) {
		return []api.ComposeValidationError{workflowdiag.EmitDefault(workflowdiag.MustCode("catalog_only_predicate"), field,
			map[string]any{"leaf": id})}
	}
	if !reg.Has(id) && !workflowdef.IsKnownGateLeaf(id) {
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

// validateExprLeaves validates an authored boolean expression string. Every
// expression is parsed; a bare leaf is a one-node tree.
func validateExprLeaves(reg *conditions.ConditionRegistry, field, expr string) []api.ComposeValidationError {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return nil
	}
	node, err := boolexpr.Parse(expr)
	if err != nil {
		return []api.ComposeValidationError{workflowdiag.EmitDefault(workflowdiag.MustCode("boolexpr_parse"), field,
			map[string]any{"detail": err.Error()})}
	}
	var out []api.ComposeValidationError
	for _, id := range boolexpr.CollectIdents(node) {
		out = append(out, validateLeafID(reg, field, id)...)
	}
	return out
}
