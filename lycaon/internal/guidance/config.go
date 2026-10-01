// Package guidance loads and projects the hint registry.
package guidance

import (
	"maps"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// HintConfig is the on-disk hint_codes YAML shape.
type HintConfig struct {
	HintCodes map[string]HintEntry `yaml:"hint_codes"`
}

// Clone copies the code map; slices and maps inside each entry stay shared.
func (c *HintConfig) Clone() *HintConfig {
	if c == nil {
		return nil
	}
	return &HintConfig{HintCodes: maps.Clone(c.HintCodes)}
}

// EvidenceCheckMeta returns a declared UI label and check id, or false when absent.
func (c *HintConfig) EvidenceCheckMeta(code string) (label, id string, ok bool) {
	if c == nil {
		return "", "", false
	}
	entry, found := c.HintCodes[strings.TrimSpace(code)]
	if !found || entry.Evidence == nil {
		return "", "", false
	}
	return entry.Evidence.UILabel, entry.Evidence.UICheckID, true
}

// IsInSessionRetry reports whether an evidence grounding code is declared
// eligible for in-session citation grounding retries (worker finalize or coordinator closeout).
func (c *HintConfig) IsInSessionRetry(code string) bool {
	if c == nil {
		return false
	}
	entry, ok := c.HintCodes[strings.TrimSpace(code)]
	return ok && entry.Evidence != nil && entry.Evidence.InSessionRetry
}

// InSessionRetryCodes returns evidence grounding codes declared eligible for
// in-session citation grounding retries, sorted for stable comparison.
func (c *HintConfig) InSessionRetryCodes() []string {
	if c == nil {
		return nil
	}
	var out []string
	for code, entry := range c.HintCodes {
		if entry.Evidence != nil && entry.Evidence.InSessionRetry {
			out = append(out, code)
		}
	}
	sort.Strings(out)
	return out
}

// HintEntry holds rule metadata and its normalized presentation fields.
type HintEntry struct {
	Emit              string          `yaml:"emit,omitempty"`
	Category          string          `yaml:"category,omitempty"`
	Severity          string          `yaml:"severity,omitempty"`
	When              string          `yaml:"when,omitempty"`
	Tools             []string        `yaml:"tools,omitempty"`
	Message           string          `yaml:"message,omitempty"`
	DraftOutcomeLabel string          `yaml:"draft_outcome_label,omitempty"`
	What              string          `yaml:"what,omitempty"`
	Cause             string          `yaml:"cause,omitempty"`
	Why               string          `yaml:"why,omitempty"`
	Fix               string          `yaml:"fix,omitempty"`
	Instead           string          `yaml:"instead,omitempty"`
	ContextSchema     map[string]any  `yaml:"context_schema,omitempty"`
	Evidence          *EvidenceMeta   `yaml:"evidence,omitempty"`
	Scenarios         []ScenarioEntry `yaml:"scenarios,omitempty"`

	ID          string         `yaml:"id,omitempty"`
	Title       string         `yaml:"title,omitempty"`
	Kind        string         `yaml:"kind,omitempty"`
	Selector    map[string]any `yaml:"selector,omitempty"`
	Effect      string         `yaml:"effect,omitempty"`
	Enforcement string         `yaml:"enforcement,omitempty"`
	Mandatory   bool           `yaml:"mandatory,omitempty"`
	OnError     string         `yaml:"on_error,omitempty"`
	OnFire      []string       `yaml:"on_fire,omitempty"`
	Flow        []string       `yaml:"flow,omitempty"`
	References  map[string]any `yaml:"references,omitempty"`
	Detector    map[string]any `yaml:"detector,omitempty"`

	OAR       string        `yaml:"oar,omitempty"`
	Anchor    string        `yaml:"anchor,omitempty"`
	Copy      *HintCopy     `yaml:"copy,omitempty"`
	Requires  *HintRequires `yaml:"requires,omitempty"`
	Overrides []string      `yaml:"overrides,omitempty"`
	Status    string        `yaml:"status,omitempty"`
	Related   []HintRelated `yaml:"related,omitempty"`
	Successor string        `yaml:"x-paintedwolf-successor,omitempty"`

	XEmit string `yaml:"x-paintedwolf-emit,omitempty"`
	// XAudience limits the tool profiles that can receive this rule.
	XAudience      []string        `yaml:"x-paintedwolf-audience,omitempty"`
	XCategory      string          `yaml:"x-paintedwolf-category,omitempty"`
	XMessage       string          `yaml:"x-paintedwolf-message,omitempty"`
	XScenarios     []ScenarioEntry `yaml:"x-paintedwolf-scenarios,omitempty"`
	XEvidence      *EvidenceMeta   `yaml:"x-paintedwolf-evidence,omitempty"`
	XContextSchema map[string]any  `yaml:"x-paintedwolf-context-schema,omitempty"`
}

// HintCopy contains presentation fields that do not affect decisions ([OAR-DOC-24]).
type HintCopy struct {
	Title   string `yaml:"title,omitempty"`
	What    string `yaml:"what,omitempty"`
	Cause   string `yaml:"cause,omitempty"`
	Why     string `yaml:"why,omitempty"`
	Fix     string `yaml:"fix,omitempty"`
	Instead string `yaml:"instead,omitempty"`
}

// HintRequires declares capability profiles and host-tier facts ([OAR-DOC-21]).
type HintRequires struct {
	Profiles []string `yaml:"profiles,omitempty"`
	Facts    []string `yaml:"facts,omitempty"`
}

// HintRelated records rule lineage without affecting decisions ([OAR-DOC-30]).
type HintRelated struct {
	ID   string `yaml:"id"`
	Type string `yaml:"type"`
}

// UnmarshalYAML decodes a rule document into its normalized shape.
func (e *HintEntry) UnmarshalYAML(node *yaml.Node) error {
	type hintAlias HintEntry
	var raw hintAlias
	if err := node.Decode(&raw); err != nil {
		return err
	}
	*e = HintEntry(raw)
	if strings.TrimSpace(e.OAR) != "" {
		e.projectOARFields()
	}
	return nil
}

// projectOARFields maps rule fields to the hint rendering model.
func (e *HintEntry) projectOARFields() {
	if c := e.Copy; c != nil {
		e.Title = c.Title
		e.What = c.What
		e.Cause = c.Cause
		e.Why = c.Why
		e.Fix = c.Fix
		e.Instead = c.Instead
	}
	e.Emit = e.XEmit
	e.Category = e.XCategory
	e.Message = e.XMessage
	e.Scenarios = e.XScenarios
	e.Evidence = e.XEvidence
	e.ContextSchema = e.XContextSchema
	e.Tools = stringList(e.Selector["tool"])
}

func stringList(v any) []string {
	items, ok := v.([]any)
	if !ok {
		if already, isStrings := v.([]string); isStrings {
			return append([]string(nil), already...)
		}
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if s, isString := item.(string); isString {
			out = append(out, s)
		}
	}
	return out
}

// EvidenceMeta configures grounding retry eligibility and citation-panel labels.
type EvidenceMeta struct {
	UILabel        string `yaml:"ui_label,omitempty"`
	UICheckID      string `yaml:"ui_check_id,omitempty"`
	InSessionRetry bool   `yaml:"in_session_retry,omitempty"`
}

// ScenarioEntry is a parametrized contract-test shape sample.
type ScenarioEntry struct {
	ID             string         `yaml:"id"`
	Tool           string         `yaml:"tool,omitempty"`
	Vars           map[string]any `yaml:"vars,omitempty"`
	ExpectContains []string       `yaml:"expect_contains,omitempty"`
	Request        map[string]any `yaml:"request,omitempty"`
}

// RejectBlock is a parsed reject block.
type RejectBlock struct {
	Code string
	What string
	// Cause is the occurrence detail required for blocking guidance.
	Cause string
	Fix   string
}
