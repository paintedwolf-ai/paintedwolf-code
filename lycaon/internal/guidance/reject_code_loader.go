package guidance

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/hintregistry"
	"github.com/lycaon/lycaon/internal/oarcopy"
	"github.com/lycaon/lycaon/internal/oarcore"
)

// RejectCodeView holds rendering metadata; Instead supplies branch_instruction.
type RejectCodeView struct {
	ID   string
	Emit string
	// Effect and Anchor distinguish blocked calls from post-call warnings.
	Effect    string
	Anchor    string
	Category  string
	Severity  string
	Tools     []string
	What      string
	Cause     string
	Why       string
	Fix       string
	Instead   string
	Scenarios []ScenarioEntry
}

// LoadHintConfig reads and merges all hint code YAML files under dir, which may
// be bundled policy or a host overlay.
func LoadHintConfig(dir extpacks.Source) (*HintConfig, error) {
	entries, err := hintregistry.List(dir)
	if err != nil {
		return nil, err
	}
	return hintConfigFromEntries(entries)
}

// LoadHintConfigStock unions policy/ across every contributing pack.
func LoadHintConfigStock() (*HintConfig, error) {
	catalog, err := extpacks.CatalogForConsumers()
	if err != nil {
		return nil, err
	}
	key := hintregistry.EffectivePolicyKey(catalog)
	if cfg, ok := effectiveHintCache.lookup(key); ok {
		return cfg, nil
	}
	entries, err := hintregistry.ListEffectiveWithCatalog(catalog)
	if err != nil {
		return nil, err
	}
	cfg, err := hintConfigFromEntries(entries)
	if err != nil {
		return nil, err
	}
	effectiveHintCache.store(key, cfg)
	return cfg, nil
}

// hintConfigCacheCap matches the policy registry's retained sets.
const hintConfigCacheCap = 4

var effectiveHintCache = hintConfigCache{configs: map[string]*HintConfig{}}

type hintConfigCache struct {
	mu      sync.Mutex
	configs map[string]*HintConfig
	order   []string
}

func (c *hintConfigCache) lookup(key string) (*HintConfig, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cfg, ok := c.configs[key]
	if !ok {
		return nil, false
	}
	return cfg.Clone(), true
}

func (c *hintConfigCache) store(key string, cfg *HintConfig) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.configs[key]; exists {
		return
	}
	if len(c.order) == hintConfigCacheCap {
		delete(c.configs, c.order[0])
		c.order = c.order[1:]
	}
	c.configs[key] = cfg.Clone()
	c.order = append(c.order, key)
}

func hintConfigFromEntries(entries []hintregistry.Entry) (*HintConfig, error) {
	merged := &HintConfig{HintCodes: map[string]HintEntry{}}
	for _, ent := range entries {
		var row HintEntry
		if err := config.DecodeYAML(ent.Body, &row); err != nil {
			return nil, fmt.Errorf("%s: %w", ent.Path, err)
		}
		merged.HintCodes[ent.Code] = row
	}
	return merged, nil
}

// LoadValidatedHintConfig reads and validates a hint registry root.
func LoadValidatedHintConfig(dir extpacks.Source) (*HintConfig, error) {
	cfg, err := LoadHintConfig(dir)
	if err != nil {
		return nil, err
	}
	if err := ValidateHintConfig(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// ValidateHintConfig enforces enriched schema + scenarios for every code.
func ValidateHintConfig(cfg *HintConfig) error {
	if cfg == nil || len(cfg.HintCodes) == 0 {
		return fmt.Errorf("hint_codes required")
	}
	var errs []string
	for code, entry := range cfg.HintCodes {
		if err := validateHintEntry(code, entry); err != nil {
			errs = append(errs, err.Error())
		}
	}
	for code, entry := range cfg.HintCodes {
		if err := validateHintSuccessors(code, entry, cfg.HintCodes); err != nil {
			errs = append(errs, err.Error())
		}
	}
	sort.Strings(errs)
	if len(errs) > 0 {
		return fmt.Errorf("hint-codes:\n%s", strings.Join(errs, "\n"))
	}
	return nil
}

func validateHintSuccessors(code string, entry HintEntry, codes map[string]HintEntry) error {
	found := strings.TrimSpace(entry.Successor) != ""
	for _, relation := range entry.Related {
		if strings.TrimSpace(relation.Type) != "obsolete" {
			continue
		}
		successor := strings.TrimSpace(relation.ID)
		if successor == code {
			return fmt.Errorf("%s: successor must not reference itself", code)
		}
		if _, ok := codes[successor]; !ok {
			return fmt.Errorf("%s: successor %q is not a known hint code", code, successor)
		}
		found = true
	}
	if strings.TrimSpace(entry.Status) == "deprecated" && !found {
		return fmt.Errorf("%s: deprecated hint requires an obsolete rule relation or x-paintedwolf-successor naming the replacement boundary", code)
	}
	return nil
}

// Copy-only emit channels. A banner rides on a tool result, a ui: target is
// drawn by Den, and an inject: target renders inside the prompt block of the
// inform anchor it names (inject:active_workflow is inject.active_workflow).
const (
	EmitBanner       = "banner"
	EmitUIPrefix     = "ui:"
	EmitInjectPrefix = "inject:"
)

// InjectAnchor returns the inform anchor an inject: emit target names.
func InjectAnchor(emit string) (string, bool) {
	target, ok := strings.CutPrefix(strings.TrimSpace(emit), EmitInjectPrefix)
	if !ok || strings.TrimSpace(target) == "" {
		return "", false
	}
	return "inject." + strings.TrimSpace(target), true
}

// CopyOnlyEmit reports whether a unit's emit channel carries informational
// copy rather than a rejection card, so it needs no rejection fields or
// rehearsal scenarios.
func CopyOnlyEmit(emit string) bool {
	emit = strings.TrimSpace(emit)
	return emit == EmitBanner || strings.HasPrefix(emit, EmitUIPrefix) || strings.HasPrefix(emit, EmitInjectPrefix)
}

func validateHintEntry(code string, entry HintEntry) error {
	copyOnly := CopyOnlyEmit(entry.Emit)
	view := NormalizeRejectCodeView(code, entry)
	if strings.TrimSpace(entry.What) == "" && strings.TrimSpace(entry.Message) == "" {
		return fmt.Errorf("%s: what or message required", code)
	}
	if strings.TrimSpace(view.What) == "" && strings.TrimSpace(view.Cause) == "" {
		return fmt.Errorf("%s: what required", code)
	}
	if !copyOnly {
		for _, field := range []struct {
			name, val string
		}{
			{"cause", view.Cause},
			{"why", view.Why},
			{"fix", view.Fix},
			{"instead", view.Instead},
		} {
			if strings.TrimSpace(field.val) == "" || field.val == "n/a" {
				return fmt.Errorf("%s: %s required", code, field.name)
			}
		}
	}
	if len(scenarioRows(entry)) == 0 && !copyOnly && entry.Severity != "warning" {
		return fmt.Errorf("%s: scenarios required (>=1)", code)
	}
	for _, sc := range scenarioRows(entry) {
		if strings.TrimSpace(sc.ID) == "" {
			return fmt.Errorf("%s: scenario id required", code)
		}
		if len(sc.ExpectContains) < 2 {
			return fmt.Errorf("%s: scenario %q expect_contains required (>=2)", code, sc.ID)
		}
	}
	if err := validateCopyFields(code, view); err != nil {
		return err
	}
	return nil
}

func validateCopyFields(code string, view RejectCodeView) error {
	for _, field := range []struct {
		name, val string
	}{
		{"what", view.What},
		{"cause", view.Cause},
		{"why", view.Why},
		{"fix", view.Fix},
		{"instead", view.Instead},
	} {
		val := strings.TrimSpace(field.val)
		if val == "" {
			continue
		}
		if !strings.Contains(val, "{{") && !strings.Contains(val, "{%") {
			continue
		}
		if _, err := oarcore.ParseCopy(val); err != nil {
			return fmt.Errorf("%s: %s copy binding: %w", code, field.name, err)
		}
	}
	return nil
}

// NormalizeRejectCodeView fills the derived prose slots: What falls back to Message,
// Cause to Message then What, Why to Fix then Cause. Instead has no fallback — a unit
// either authors a branch instruction or emits none.
func NormalizeRejectCodeView(code string, entry HintEntry) RejectCodeView {
	view := RejectCodeView{
		ID:        strings.TrimSpace(code),
		Emit:      strings.TrimSpace(entry.Emit),
		Effect:    strings.TrimSpace(entry.Effect),
		Anchor:    strings.TrimSpace(entry.Anchor),
		Category:  strings.TrimSpace(entry.Category),
		Severity:  strings.TrimSpace(entry.Severity),
		Tools:     append([]string(nil), entry.Tools...),
		What:      strings.TrimSpace(entry.What),
		Cause:     strings.TrimSpace(entry.Cause),
		Why:       strings.TrimSpace(entry.Why),
		Fix:       strings.TrimSpace(entry.Fix),
		Instead:   strings.TrimSpace(entry.Instead),
		Scenarios: scenarioRows(entry),
	}
	msg := strings.TrimSpace(entry.Message)
	if view.What == "" {
		view.What = msg
	}
	if view.Cause == "" {
		view.Cause = msg
		if view.Cause == "" {
			view.Cause = view.What
		}
	}
	if view.Why == "" {
		view.Why = view.Fix
		if view.Why == "" {
			view.Why = view.Cause
		}
	}
	return view
}

func scenarioRows(entry HintEntry) []ScenarioEntry {
	return append([]ScenarioEntry(nil), entry.Scenarios...)
}

// ScenarioVars builds scenario variables with the selected tool.
func ScenarioVars(entry HintEntry, sc ScenarioEntry) map[string]any {
	out := map[string]any{}
	for k, v := range sc.Vars {
		out[k] = v
	}
	if _, ok := out["tool"]; !ok {
		if tool := scenarioTool(entry, sc); tool != "" {
			out["tool"] = tool
		}
	}
	return out
}

// scenarioTool picks the tool a scenario rehearses: the one it names, else the
// first tool its card selects. A card with no selector rehearses without a
// tool, which renders the same way production does for its non-tool callers.
func scenarioTool(entry HintEntry, sc ScenarioEntry) string {
	if tool := strings.TrimSpace(sc.Tool); tool != "" {
		return tool
	}
	for _, tool := range entry.Tools {
		if tool = strings.TrimSpace(tool); tool != "" {
			return tool
		}
	}
	return ""
}

// RenderUnifiedRejectBlock renders the single reject template for a code.
func RenderUnifiedRejectBlock(ctx context.Context, code string, entry HintEntry, data map[string]any) (string, error) {
	view := NormalizeRejectCodeView(code, entry)
	payload := map[string]any{
		"code":     view.ID,
		"emit":     view.Emit,
		"effect":   view.Effect,
		"anchor":   view.Anchor,
		"category": view.Category,
		"what":     renderFieldTemplate(view.What, data),
		"cause":    renderFieldTemplate(view.Cause, data),
		"why":      renderFieldTemplate(view.Why, data),
		"fix":      renderFieldTemplate(view.Fix, data),
		"instead":  renderFieldTemplate(view.Instead, data),
	}
	return RenderGuidance(ctx, "reject/_reject", payload)
}

func renderFieldTemplate(tmpl string, ctx map[string]any) string {
	tmpl = strings.TrimSpace(tmpl)
	if tmpl == "" {
		return tmpl
	}
	return strings.TrimSpace(oarcore.RenderCopy(tmpl, oarcopy.FactsFromData(ctx)))
}
