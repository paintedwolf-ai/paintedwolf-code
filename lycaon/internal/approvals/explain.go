package approvals

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/approvalregistry"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/pongoplain"
	"github.com/lycaon/lycaon/internal/settings"
)

// Registry holds loaded approval explanation entries.
type Registry struct {
	entries map[string]ExplanationEntry
}

// LoadRegistryStock loads the effective approval registry.
func LoadRegistryStock() (*Registry, error) {
	cfg, err := loadConfigStock()
	if err != nil {
		return nil, err
	}
	return newRegistry(cfg)
}

func newRegistry(cfg *Config) (*Registry, error) {
	if err := ValidateConfig(cfg); err != nil {
		return nil, err
	}
	if _, ok := cfg.Explanations[FallbackExplanationKey]; !ok {
		return nil, fmt.Errorf("approval explanation %q required", FallbackExplanationKey)
	}
	return &Registry{entries: cfg.Explanations}, nil
}

func loadConfigStock() (*Config, error) {
	entries, err := approvalregistry.ListEffective()
	if err != nil {
		return nil, err
	}
	return configFromEntries(entries)
}

func configFromEntries(entries []approvalregistry.Entry) (*Config, error) {
	merged := &Config{Explanations: map[string]ExplanationEntry{}}
	for _, ent := range entries {
		var fragment Config
		if err := config.DecodeYAML(ent.Data, &fragment); err != nil {
			return nil, fmt.Errorf("%s: %w", ent.Path, err)
		}
		for key, row := range fragment.Explanations {
			merged.Explanations[key] = row
		}
	}
	return merged, nil
}

// LoadConfigStock loads effective approval copy.
func LoadConfigStock() (*Config, error) {
	cfg, err := loadConfigStock()
	if err != nil {
		return nil, err
	}
	if err := ValidateConfig(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Keys returns sorted registry keys.
func (r *Registry) Keys() []string {
	if r == nil {
		return nil
	}
	out := make([]string, 0, len(r.entries))
	for k := range r.entries {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Entry returns one explanation entry by key.
func (r *Registry) Entry(key string) (ExplanationEntry, bool) {
	if r == nil {
		return ExplanationEntry{}, false
	}
	entry, ok := r.entries[key]
	return entry, ok
}

// ExplainAction renders reviewed copy for a proposed action.
func (r *Registry) ExplainAction(action hitl.ProposedAction, tier settings.ReversibilityTier) ExplanationResult {
	key := ResolveExplanationKey(action, tier)
	entry, ok := r.entries[key]
	usedFallback := !ok
	if !ok {
		entry = r.entries[FallbackExplanationKey]
	}
	vars := ActionTemplateVars(action)
	return ExplanationResult{
		Key:          key,
		UsedFallback: usedFallback,
		Copy:         RenderEntry(entry, vars),
	}
}

// RenderEntry renders all slots for an entry with vars.
func RenderEntry(entry ExplanationEntry, vars map[string]any) ExplanationCopy {
	return ExplanationCopy{
		What:      renderTemplate(entry.WhatChanges, vars),
		Who:       renderTemplate(entry.WhoAffected, vars),
		IfWrong:   renderTemplate(entry.IfWrong, vars),
		AllowLine: renderTemplate(entry.AllowLine, vars),
	}
}

func renderTemplate(tmpl string, ctx map[string]any) string {
	out, err := renderTemplateChecked(tmpl, ctx)
	if err != nil {
		return ""
	}
	return out
}

func renderTemplateChecked(tmpl string, ctx map[string]any) (string, error) {
	tmpl = strings.TrimSpace(tmpl)
	if tmpl == "" {
		return "", nil
	}
	if !strings.Contains(tmpl, "{{") && !strings.Contains(tmpl, "{%") {
		return tmpl, nil
	}
	parsed, err := pongoplain.Compile(tmpl)
	if err != nil {
		return "", err
	}
	out, err := pongoplain.Execute(context.Background(), parsed, ctx)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// ScenarioVars returns render context for a scenario row.
func ScenarioVars(sc ScenarioEntry) map[string]any {
	out := map[string]any{}
	for k, v := range sc.Vars {
		out[k] = v
	}
	if len(out) == 0 {
		out["tool"] = "read"
	}
	return out
}
