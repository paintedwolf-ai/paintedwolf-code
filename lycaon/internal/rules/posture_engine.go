package rules

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/pkg/api"
)

// PostureRulesSource resolves bundled rule file paths for a session posture.
type PostureRulesSource interface {
	RulesPaths(posture api.SessionPosture) ([]string, error)
}

// PostureRuleEngine evaluates only the rule files bound to Session.posture.
type PostureRuleEngine struct {
	Postures    PostureRulesSource
	RulesByPath map[string]*RulesConfig
	Registry    *conditions.ConditionRegistry
	Overlay     *ProjectRulesOverlay
	// ProjectSettingsApply controls project rules. Nil is closed.
	ProjectSettingsApply func(ctx context.Context, projectID string) bool
}

// NewPostureRuleEngine wires posture-scoped rule evaluation for Prompt tool gating.
func NewPostureRuleEngine(postures PostureRulesSource, rulesByPath map[string]*RulesConfig, registry *conditions.ConditionRegistry) (*PostureRuleEngine, error) {
	if postures == nil {
		return nil, fmt.Errorf("posture rules source required")
	}
	if len(rulesByPath) == 0 {
		return nil, fmt.Errorf("bundled rules required")
	}
	if registry == nil {
		return nil, fmt.Errorf("condition registry required")
	}
	if err := validateBundledRules(registry, rulesByPath); err != nil {
		return nil, err
	}
	return &PostureRuleEngine{
		Postures:    postures,
		RulesByPath: rulesByPath,
		Registry:    registry,
	}, nil
}

// validateBundledRules rejects a rule whose when clause cannot resolve, so a
// resolution failure during evaluation is fatal.
func validateBundledRules(registry *conditions.ConditionRegistry, rulesByPath map[string]*RulesConfig) error {
	paths := make([]string, 0, len(rulesByPath))
	for path := range rulesByPath {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		cfg := rulesByPath[path]
		if cfg == nil {
			continue
		}
		for i, rule := range cfg.Rules {
			if err := ValidateWhen(registry, rule.When); err != nil {
				return fmt.Errorf("%s rule %d: %w", path, i, err)
			}
		}
	}
	return nil
}

// Evaluate runs ordered rules from posture-bound packs, manifest rule paths, then project overlay.
//
// Rule file precedence:
//  1. PostureRules from effective posture registry (EvalContext, includes project overlay)
//  2. ManifestRules from active workflow manifest (EvalContext, appended, deduped)
//  3. Project <overlay>/rules/* overlay (deny overrides bundled allow)
func (e *PostureRuleEngine) Evaluate(ctx context.Context, eval EvalContext) (*RuleOutcome, error) {
	if e == nil {
		return &RuleOutcome{Allowed: true}, nil
	}
	if eval.SessionPosture == "" {
		return &RuleOutcome{
			Allowed: false,
			Code:    "SPEC_POSTURE_UNRESOLVED",
			Message: "session posture required before tool execution",
		}, nil
	}
	paths := append([]string(nil), eval.PostureRules...)
	if len(paths) == 0 {
		var err error
		paths, err = e.Postures.RulesPaths(eval.SessionPosture)
		if err != nil {
			return &RuleOutcome{ //nolint:nilerr // err.Error() carried as RuleOutcome.Message
				Allowed: false,
				Code:    "POSTURE_UNKNOWN",
				Message: err.Error(),
			}, nil
		}
	}
	paths = MergeRulesPaths(paths, eval.ManifestRules)
	rules, err := e.rulesForPaths(paths)
	if err != nil {
		return nil, err
	}
	engine := &SimpleEngine{rules: rules, registry: e.Registry}
	outcome, err := engine.Evaluate(ctx, eval)
	if err != nil {
		return nil, err
	}
	if outcome != nil && !outcome.Allowed {
		return outcome, nil
	}
	if e.Overlay != nil && e.projectSettingsApply(ctx, eval) {
		hasOverlay := len(eval.OverlayRootPaths) > 0 || strings.TrimSpace(eval.ProjectDir) != ""
		if hasOverlay {
			overlayOutcome, err := e.Overlay.Evaluate(e.Registry, eval)
			if err != nil {
				return nil, err
			}
			if overlayOutcome != nil && !overlayOutcome.Allowed {
				return overlayOutcome, nil
			}
		}
	}
	return outcome, nil
}

func (e *PostureRuleEngine) rulesForPaths(paths []string) ([]RuleEntry, error) {
	var out []RuleEntry
	for _, raw := range paths {
		key := NormalizeRulesPath(raw)
		cfg, ok := e.RulesByPath[key]
		if !ok {
			return nil, fmt.Errorf("missing rule file %q for posture", key)
		}
		out = append(out, cfg.Rules...)
	}
	return out, nil
}

func (e *PostureRuleEngine) projectSettingsApply(ctx context.Context, eval EvalContext) bool {
	if e == nil || e.ProjectSettingsApply == nil {
		return false
	}
	projectID := strings.TrimSpace(eval.ProjectID)
	if projectID == "" {
		return false
	}
	return e.ProjectSettingsApply(ctx, projectID)
}

// NormalizeRulesPath canonicalizes a declared rule path to its config-relative
// form, the loaded index key. A leading "config/" maps to the same key.
func NormalizeRulesPath(path string) string {
	path = filepath.ToSlash(strings.TrimSpace(path))
	return strings.TrimPrefix(path, "config/")
}

// MergeRulesPaths appends manifest rule paths after posture paths with stable dedupe.
func MergeRulesPaths(posturePaths, manifestPaths []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, list := range [][]string{posturePaths, manifestPaths} {
		for _, raw := range list {
			key := NormalizeRulesPath(raw)
			if key == "" {
				continue
			}
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, key)
		}
	}
	return out
}

// ValidatePostureRules ensures every posture rules path exists in the loaded rule index.
func ValidatePostureRules(postures PostureRulesSource, allPostures []api.SessionPosture, rulesByPath map[string]*RulesConfig) error {
	if postures == nil {
		return fmt.Errorf("posture rules source required")
	}
	for _, posture := range allPostures {
		paths, err := postures.RulesPaths(posture)
		if err != nil {
			return fmt.Errorf("posture %q: %w", posture, err)
		}
		if len(paths) == 0 {
			return fmt.Errorf("posture %q: no rules paths", posture)
		}
		for _, p := range paths {
			key := NormalizeRulesPath(p)
			if _, ok := rulesByPath[key]; !ok {
				return fmt.Errorf("posture %q: rule file %q not loaded", posture, key)
			}
		}
	}
	return ValidateRulesIndex(rulesByPath)
}

// ValidateRulesIndex rejects duplicate rule ids across bundled rule files.
func ValidateRulesIndex(rulesByPath map[string]*RulesConfig) error {
	seen := map[string]string{}
	for path, cfg := range rulesByPath {
		if cfg == nil {
			return fmt.Errorf("nil rules config at %q", path)
		}
		for _, rule := range cfg.Rules {
			if prev, ok := seen[rule.ID]; ok {
				return fmt.Errorf("duplicate rule id %q in %s and %s", rule.ID, prev, path)
			}
			seen[rule.ID] = path
		}
	}
	return nil
}
