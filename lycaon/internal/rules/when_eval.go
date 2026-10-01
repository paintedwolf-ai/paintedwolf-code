package rules

import (
	"errors"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/boolexpr"
	"github.com/lycaon/lycaon/internal/conditions"
)

// MatchWhen evaluates a rule map through the condition registry. Every leaf must
// hold: a when map is a conjunction.
func MatchWhen(reg *conditions.ConditionRegistry, when map[string]any, eval EvalContext) (bool, error) {
	if len(when) == 0 {
		return false, nil
	}
	if reg == nil {
		return false, fmt.Errorf("nil condition registry")
	}
	leaves, err := CanonicalWhenLeaves(when)
	if err != nil {
		return false, err
	}
	env, firstErr := registryEnv(reg, eval)
	matched := true
	// No short-circuit: a later leaf naming an unregistered condition is still an
	// authoring error once an earlier one has answered false.
	for _, leaf := range leaves {
		if env(leaf.Name) == leaf.Negated {
			matched = false
		}
	}
	if err := firstErr(); err != nil {
		return false, fmt.Errorf("when %q: %w", renderLeaves(leaves), err)
	}
	return matched, nil
}

// ValidateWhen reports whether a rule's when map resolves in the registry.
func ValidateWhen(reg *conditions.ConditionRegistry, when map[string]any) error {
	if len(when) == 0 {
		return fmt.Errorf("empty when map")
	}
	if reg == nil {
		return fmt.Errorf("nil condition registry")
	}
	leaves, err := CanonicalWhenLeaves(when)
	if err != nil {
		return err
	}
	for _, leaf := range leaves {
		if err := validateLeafName(reg, leaf.Name); err != nil {
			return fmt.Errorf("when %q: %w", renderLeaves(leaves), err)
		}
	}
	return nil
}

func renderLeaves(leaves []WhenLeaf) string {
	parts := make([]string, 0, len(leaves))
	for _, l := range leaves {
		parts = append(parts, l.String())
	}
	return strings.Join(parts, " and ")
}

func validateLeafName(reg *conditions.ConditionRegistry, name string) error {
	if conditions.IsForbidden(name) {
		return fmt.Errorf("%w: %s", conditions.ErrForbiddenCondition, name)
	}
	if !reg.Has(name) {
		return fmt.Errorf("%w: %s", conditions.ErrUnknownCondition, name)
	}
	return nil
}

// MatchWhenExpr evaluates a boolean when expression string via the registry.
// Every expression is parsed; a lone condition name is a one-node tree.
func MatchWhenExpr(reg *conditions.ConditionRegistry, expr string, eval EvalContext) (bool, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return false, fmt.Errorf("empty when expression")
	}
	if reg == nil {
		return false, fmt.Errorf("nil condition registry")
	}
	node, err := boolexpr.Parse(expr)
	if err != nil {
		return false, fmt.Errorf("parse when %q: %w", expr, err)
	}
	env, firstErr := registryEnv(reg, eval)
	matched := boolexpr.Eval(node, env)
	if err := firstErr(); err != nil {
		return false, fmt.Errorf("when %q: %w", expr, err)
	}
	return matched, nil
}

// ValidateWhenExpr reports whether every condition an expression names resolves.
// It checks name resolution rather than evaluating: a registered condition may
// fail against an empty context without the rule being wrong.
func ValidateWhenExpr(reg *conditions.ConditionRegistry, expr string) error {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return fmt.Errorf("empty when expression")
	}
	if reg == nil {
		return fmt.Errorf("nil condition registry")
	}
	node, err := boolexpr.Parse(expr)
	if err != nil {
		return fmt.Errorf("parse when %q: %w", expr, err)
	}
	for _, key := range boolexpr.CollectIdents(node) {
		if err := validateLeafName(reg, key); err != nil {
			return fmt.Errorf("when %q: %w", expr, err)
		}
	}
	return nil
}

// registryEnv adapts the condition registry to boolexpr's name→bool environment.
// An unregistered name is kept for the caller to raise: the rule can never match.
// A condition that ran and failed only reads false, so one unreadable fact does
// not reject the tool call.
func registryEnv(
	reg *conditions.ConditionRegistry, eval EvalContext,
) (env func(string) bool, firstErr func() error) {
	var stored error
	return func(name string) bool {
			ok, err := reg.Evaluate(name, eval.EvalContext)
			if err != nil && stored == nil && isAuthoringError(err) {
				stored = err
			}
			return err == nil && ok
		}, func() error {
			return stored
		}
}

// isAuthoringError reports whether a registry error means the rule text is wrong
// rather than the fact being unavailable.
func isAuthoringError(err error) bool {
	return errors.Is(err, conditions.ErrUnknownCondition) ||
		errors.Is(err, conditions.ErrForbiddenCondition)
}
