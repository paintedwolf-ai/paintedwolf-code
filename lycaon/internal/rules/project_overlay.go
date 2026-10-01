package rules

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"gopkg.in/yaml.v3"
)

// ProjectRules is the on-disk project rule document.
type ProjectRules struct {
	Rules []RuleDefinition `yaml:"rules"`
}

// RuleDefinition is one machine-evaluated project deny rule.
type RuleDefinition struct {
	When          string `yaml:"when"`
	Code          string `yaml:"code"`
	Message       string `yaml:"message,omitempty"`
	PhaseRequired string `yaml:"phase_required,omitempty"`
}

// ProjectRulesDir is <overlay>/rules under a project root.
func ProjectRulesDir(projectDir string) string {
	return filepath.Join(projectDir, settingsoverlay.DirName(), "rules")
}

// cachedProjectRules binds a merge to its source fingerprint.
type cachedProjectRules struct {
	fingerprint string
	rules       ProjectRules
}

// ProjectRulesOverlay revalidates cached rules by content on every read.
type ProjectRulesOverlay struct {
	mu       sync.RWMutex
	registry *conditions.ConditionRegistry
	cache    map[string]cachedProjectRules
}

// NewProjectRulesOverlay creates a project rule evaluator.
func NewProjectRulesOverlay(registry *conditions.ConditionRegistry) *ProjectRulesOverlay {
	return &ProjectRulesOverlay{
		registry: registry,
		cache:    make(map[string]cachedProjectRules),
	}
}

// rulesFingerprint identifies the exact files that produced a cached merge.
func rulesFingerprint(rootPaths []string) (string, error) {
	var b strings.Builder
	for _, rootPath := range rootPaths {
		rootPath = strings.TrimSpace(rootPath)
		if rootPath == "" {
			continue
		}
		dir := ProjectRulesDir(rootPath)
		b.WriteString(filepath.Clean(rootPath))
		b.WriteByte('\x00')
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				b.WriteString("absent\x00")
				continue
			}
			return "", fmt.Errorf("read project rules directory %s: %w", dir, err)
		}
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			if e.IsDir() || filepath.Ext(e.Name()) != ".yaml" {
				continue
			}
			names = append(names, e.Name())
		}
		sort.Strings(names)
		for _, name := range names {
			path := filepath.Join(dir, name)
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				return "", fmt.Errorf("read project rules file %s: %w", path, readErr)
			}
			sum := sha256.Sum256(data)
			fmt.Fprintf(&b, "%s:%x\x00", name, sum)
		}
	}
	return b.String(), nil
}

func currentRulesFingerprint(rootPaths []string) (string, error) {
	if err := settingsoverlay.CheckFormats(rootPaths); err != nil {
		return "", err
	}
	return rulesFingerprint(rootPaths)
}

func (o *ProjectRulesOverlay) loadStable(rootPaths []string) (ProjectRules, string, error) {
	const attempts = 3
	for range attempts {
		before, err := currentRulesFingerprint(rootPaths)
		if err != nil {
			return ProjectRules{}, "", err
		}
		rules, err := loadProjectRulesFromOverlayRoots(rootPaths)
		if err != nil {
			return ProjectRules{}, "", err
		}
		if err := validateProjectRules(o.registry, rules.Rules); err != nil {
			return ProjectRules{}, "", err
		}
		after, err := currentRulesFingerprint(rootPaths)
		if err != nil {
			return ProjectRules{}, "", err
		}
		if before == after {
			return rules, after, nil
		}
	}
	return ProjectRules{}, "", fmt.Errorf("project rules changed while loading")
}

// WarmOverlays validates and caches rules from ordered overlay roots.
func (o *ProjectRulesOverlay) WarmOverlays(rootPaths []string) error {
	if o == nil {
		return nil
	}
	key := project.OverlayCacheKey(rootPaths)
	if key == "" {
		return nil
	}
	rules, fingerprint, err := o.loadStable(rootPaths)
	if err != nil {
		return err
	}
	o.mu.Lock()
	o.cache[key] = cachedProjectRules{fingerprint: fingerprint, rules: rules}
	o.mu.Unlock()
	return nil
}

// GetOverlays returns merged project rules for ordered overlay roots.
func (o *ProjectRulesOverlay) GetOverlays(rootPaths []string) (ProjectRules, error) {
	if o == nil {
		return ProjectRules{}, nil
	}
	key := project.OverlayCacheKey(rootPaths)
	if key == "" {
		return ProjectRules{}, nil
	}
	fingerprint, err := currentRulesFingerprint(rootPaths)
	if err != nil {
		return ProjectRules{}, err
	}
	o.mu.RLock()
	cached, ok := o.cache[key]
	o.mu.RUnlock()
	if ok && cached.fingerprint == fingerprint {
		return cloneProjectRules(cached.rules), nil
	}

	rules, fingerprint, err := o.loadStable(rootPaths)
	if err != nil {
		return ProjectRules{}, err
	}
	o.mu.Lock()
	o.cache[key] = cachedProjectRules{fingerprint: fingerprint, rules: rules}
	o.mu.Unlock()
	return cloneProjectRules(rules), nil
}

// Evaluate applies project overlay rules after bundled posture/manifest evaluation.
// Project deny rules override bundled allow outcomes.
func (o *ProjectRulesOverlay) Evaluate(reg *conditions.ConditionRegistry, eval EvalContext) (*RuleOutcome, error) {
	if o == nil {
		return &RuleOutcome{Allowed: true}, nil
	}
	rootPaths := eval.OverlayRootPaths
	if len(rootPaths) == 0 && strings.TrimSpace(eval.ProjectDir) != "" {
		rootPaths = []string{eval.ProjectDir}
	}
	if len(rootPaths) == 0 {
		return &RuleOutcome{Allowed: true}, nil
	}
	rules, err := o.GetOverlays(rootPaths)
	if err != nil {
		return nil, err
	}
	if reg == nil {
		reg = o.registry
	}
	for _, def := range rules.Rules {
		ok, err := MatchWhenExpr(reg, def.When, eval)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		msg := def.Message
		if msg == "" {
			msg = def.Code
		}
		return &RuleOutcome{Allowed: false, Code: def.Code, Message: msg}, nil
	}
	return &RuleOutcome{Allowed: true}, nil
}

func loadProjectRulesFromOverlayRoots(rootPaths []string) (ProjectRules, error) {
	var merged []RuleDefinition
	for _, rootPath := range rootPaths {
		rootPath = strings.TrimSpace(rootPath)
		if rootPath == "" {
			continue
		}
		cfg, err := loadProjectRulesFromDir(rootPath)
		if err != nil {
			return ProjectRules{}, err
		}
		merged = mergeProjectRuleDefinitions(merged, cfg.Rules)
	}
	return ProjectRules{Rules: merged}, nil
}

func loadProjectRulesFromDir(projectDir string) (ProjectRules, error) {
	dir := ProjectRulesDir(projectDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return ProjectRules{}, nil
		}
		return ProjectRules{}, err
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".yaml" {
			continue
		}
		files = append(files, e.Name())
	}
	sort.Strings(files)

	var merged []RuleDefinition
	for _, name := range files {
		cfg, err := loadProjectRulesFile(filepath.Join(dir, name))
		if err != nil {
			return ProjectRules{}, fmt.Errorf("%s: %w", name, err)
		}
		merged = mergeProjectRuleDefinitions(merged, cfg.Rules)
	}
	return ProjectRules{Rules: merged}, nil
}

func loadProjectRulesFile(path string) (ProjectRules, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ProjectRules{}, err
	}
	var cfg ProjectRules
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return ProjectRules{}, fmt.Errorf("parse project rules: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return ProjectRules{}, fmt.Errorf("parse project rules: multiple YAML documents are not allowed")
		}
		return ProjectRules{}, fmt.Errorf("parse project rules: %w", err)
	}
	return normalizeProjectRules(cfg), nil
}

func normalizeProjectRules(cfg ProjectRules) ProjectRules {
	out := make([]RuleDefinition, 0, len(cfg.Rules))
	for _, r := range cfg.Rules {
		r.When = strings.TrimSpace(r.When)
		r.Code = strings.TrimSpace(r.Code)
		r.Message = strings.TrimSpace(r.Message)
		r.PhaseRequired = strings.TrimSpace(r.PhaseRequired)
		out = append(out, r)
	}
	return ProjectRules{Rules: out}
}

func projectRuleKey(r RuleDefinition) string {
	return r.When + "\x00" + r.Code
}

func mergeProjectRuleDefinitions(base, overlay []RuleDefinition) []RuleDefinition {
	byKey := make(map[string]RuleDefinition, len(base)+len(overlay))
	order := make([]string, 0, len(base)+len(overlay))
	for _, r := range base {
		k := projectRuleKey(r)
		if _, ok := byKey[k]; !ok {
			order = append(order, k)
		}
		byKey[k] = r
	}
	for _, r := range overlay {
		k := projectRuleKey(r)
		if _, ok := byKey[k]; !ok {
			order = append(order, k)
		}
		byKey[k] = r
	}
	out := make([]RuleDefinition, 0, len(order))
	for _, k := range order {
		out = append(out, byKey[k])
	}
	return out
}

func validateProjectRules(reg *conditions.ConditionRegistry, defs []RuleDefinition) error {
	for i, def := range defs {
		if def.When == "" {
			return fmt.Errorf("rule %d: when is required", i)
		}
		if def.Code == "" {
			return fmt.Errorf("rule %d: code is required", i)
		}
		if err := ValidateWhenExpr(reg, def.When); err != nil {
			return fmt.Errorf("rule %d (%s): %w", i, def.Code, err)
		}
	}
	return nil
}

func cloneProjectRules(in ProjectRules) ProjectRules {
	out := make([]RuleDefinition, len(in.Rules))
	copy(out, in.Rules)
	return ProjectRules{Rules: out}
}
