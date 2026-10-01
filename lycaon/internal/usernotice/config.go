// Package usernotice loads user-facing notice copy for Den (host/user-notices units).
package usernotice

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/config"
)

const defaultsStem = "defaults"

var allowedSurfaces = map[string]struct{}{
	"host_error":     {},
	"http":           {},
	"worker_failure": {},
	"preflight":      {},
}

var allowedActions = map[string]struct{}{
	"open_ai_providers":        {},
	"retry_contribution_frame": {},
	"prompt_retry":             {},
	"prompt_keep_going":        {},
	"prompt_rewind_and_retry":  {},
}

// ContextTurnProgress is stamped by the host only on prompt notices, so
// actions conditioned on it never render for other delivery surfaces.
const ContextTurnProgress = "turn_progress"

// Turn progress values. Progress is "none" only when the transcript shows the
// failed turn produced no assistant or tool output.
const (
	TurnProgressNone = "none"
	TurnProgressMade = "made"
)

// hostContextDomains lists host-stamped variables with every value they take;
// templated actions are validated with each value and with the variable absent.
var hostContextDomains = map[string][]any{
	ContextTurnProgress: {TurnProgressNone, TurnProgressMade},
}

// Config is the merged in-memory user-notice catalog.
type Config struct {
	Defaults    NoticeCopy       `yaml:"defaults"`
	UserNotices map[string]Entry `yaml:"user_notices"`
}

// NoticeCopy is title/message/fix text shown on the notice rail.
type NoticeCopy struct {
	Title           string   `yaml:"title"`
	Message         string   `yaml:"message"`
	SuggestedAction string   `yaml:"suggested_action"`
	Actions         []string `yaml:"actions,omitempty"`
}

// Entry is one user-notice unit body keyed by API error code.
// On disk, unit files place these fields at the document root (no user_notices: wrapper).
type Entry struct {
	UserVisible     *bool           `yaml:"user_visible"`
	UseDefaults     *bool           `yaml:"use_defaults"`
	Retryable       bool            `yaml:"retryable"`
	Surfaces        []string        `yaml:"surfaces"`
	Notification    *Notification   `yaml:"notification"`
	ContextSchema   map[string]any  `yaml:"context_schema"`
	Title           string          `yaml:"title"`
	Message         string          `yaml:"message"`
	SuggestedAction string          `yaml:"suggested_action"`
	Action          string          `yaml:"action,omitempty"`
	Actions         []string        `yaml:"actions,omitempty"`
	Scenarios       []ScenarioEntry `yaml:"scenarios"`
}

// ScenarioEntry is one notice render fixture.
type ScenarioEntry struct {
	ID             string         `yaml:"id"`
	Resolution     string         `yaml:"resolution"`
	Vars           map[string]any `yaml:"vars"`
	ExpectContains []string       `yaml:"expect_contains"`
}

// IsUserVisible reports whether this code may appear on the notice rail.
func (e Entry) IsUserVisible() bool {
	if e.UserVisible != nil {
		return *e.UserVisible
	}
	return true
}

// HasSurface reports whether the entry is registered for a delivery surface.
func (e Entry) HasSurface(surface string) bool {
	for _, s := range e.Surfaces {
		if s == surface {
			return true
		}
	}
	return false
}

// IsRetryable reports whether retrying the same HTTP operation may succeed
// without changing its request.
func (e Entry) IsRetryable() bool {
	return e.Retryable
}

// UsesDefaults reports whether copy comes from the document-level defaults block.
func (e Entry) UsesDefaults() bool {
	return e.UseDefaults != nil && *e.UseDefaults
}

// ValidNoticeCode reports whether code is a legal host/user-notices/<code> stem
// (excluding the reserved defaults unit).
func ValidNoticeCode(code string) bool {
	code = strings.TrimSpace(code)
	if code == "" || code == defaultsStem {
		return false
	}
	return isErrorCodeShape(code)
}

// ParseEntry unmarshals a single-code unit body (Entry at document root).
func ParseEntry(data []byte) (Entry, error) {
	var entry Entry
	if err := config.DecodeYAML(data, &entry); err != nil {
		return Entry{}, err
	}
	return entry, nil
}

// ParseDefaults unmarshals the reserved defaults unit (NoticeCopy at document root).
func ParseDefaults(data []byte) (NoticeCopy, error) {
	var copy NoticeCopy
	if err := config.DecodeYAML(data, &copy); err != nil {
		return NoticeCopy{}, err
	}
	if err := requireNoticeCopy("defaults", copy); err != nil {
		return NoticeCopy{}, err
	}
	return copy, nil
}

// ConfigFromParts builds Config from defaults + code entries and validates.
func ConfigFromParts(defaults NoticeCopy, notices map[string]Entry) (*Config, error) {
	out := make(map[string]Entry, len(notices))
	for code, entry := range notices {
		out[code] = entry
	}
	cfg := &Config{Defaults: defaults, UserNotices: out}
	if err := Validate(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// LoadNoticeDir walks dir for defaults.yaml + <code>.yaml Entry-at-root files.
func LoadNoticeDir(dir string) (*Config, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var defaults NoticeCopy
	haveDefaults := false
	notices := make(map[string]Entry)
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		name := ent.Name()
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			continue
		}
		stem := strings.TrimSuffix(strings.TrimSuffix(name, ".yaml"), ".yml")
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		if stem == defaultsStem {
			defaults, err = ParseDefaults(data)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
			haveDefaults = true
			continue
		}
		if !ValidNoticeCode(stem) {
			return nil, fmt.Errorf("invalid user notice stem %q in %s", stem, dir)
		}
		entry, err := ParseEntry(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if _, dup := notices[stem]; dup {
			return nil, fmt.Errorf("duplicate user notice stem %q in %s", stem, dir)
		}
		notices[stem] = entry
	}
	if !haveDefaults {
		return nil, fmt.Errorf("host/user-notices/defaults required")
	}
	return ConfigFromParts(defaults, notices)
}

// Validate checks structural invariants before codegen or contract tests run.
func Validate(cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("user notice config is nil")
	}
	if err := requireNoticeCopy("defaults", cfg.Defaults); err != nil {
		return err
	}
	if err := validateActions("defaults", cfg.Defaults.Actions, nil); err != nil {
		return err
	}
	if err := validatePongoFields("defaults", cfg.Defaults); err != nil {
		return err
	}
	if err := validatePongoRender("defaults", cfg.Defaults, nil); err != nil {
		return err
	}
	if len(cfg.UserNotices) == 0 {
		return fmt.Errorf("user_notices must not be empty")
	}
	for code, entry := range cfg.UserNotices {
		if !isErrorCodeShape(code) {
			return fmt.Errorf("user_notices.%q: invalid code shape", code)
		}
		for _, surface := range entry.Surfaces {
			if _, ok := allowedSurfaces[surface]; !ok {
				return fmt.Errorf("user_notices.%q: unknown surface %q", code, surface)
			}
		}
		if err := validateNotification(code, entry); err != nil {
			return err
		}
		if !entry.IsUserVisible() {
			if len(entry.Surfaces) > 0 {
				return fmt.Errorf("user_notices.%q: user_visible:false must not list surfaces", code)
			}
			if entry.UsesDefaults() {
				return fmt.Errorf("user_notices.%q: user_visible:false must not set use_defaults", code)
			}
			continue
		}
		if entry.UsesDefaults() {
			if len(entry.Surfaces) == 0 {
				return fmt.Errorf("user_notices.%q: use_defaults entries must declare surfaces", code)
			}
			if strings.TrimSpace(entry.Title) != "" ||
				strings.TrimSpace(entry.Message) != "" ||
				strings.TrimSpace(entry.SuggestedAction) != "" ||
				strings.TrimSpace(entry.Action) != "" ||
				len(entry.Actions) > 0 {
				return fmt.Errorf("user_notices.%q: use_defaults entries must not set title/message/suggested_action/action/actions", code)
			}
			continue
		}
		if err := requireNoticeCopy("user_notices."+code, noticeCopyFromEntry(entry)); err != nil {
			return err
		}
		if len(entry.Surfaces) == 0 {
			return fmt.Errorf("user_notices.%q: visible entries must declare surfaces", code)
		}
		if err := validatePongoFields(code, noticeCopyFromEntry(entry)); err != nil {
			return err
		}
		if err := validateActions(code, noticeCopyFromEntry(entry).Actions, entry.Scenarios); err != nil {
			return err
		}
		for _, scenario := range entry.Scenarios {
			if err := validatePongoRender(code+"."+scenario.ID, noticeCopyFromEntry(entry), scenario.Vars); err != nil {
				return err
			}
		}
	}
	return nil
}

// WorkerFailureCodes returns sorted codes registered for SSE worker.failure.
func WorkerFailureCodes(cfg *Config) []string {
	return surfaceCodes(cfg, "worker_failure")
}

// HostErrorCodes returns sorted codes registered for SSE session.host_error.
func HostErrorCodes(cfg *Config) []string {
	return surfaceCodes(cfg, "host_error")
}

// HTTPCodes returns sorted codes registered for HTTP ErrorResponse rendering.
func HTTPCodes(cfg *Config) []string {
	return surfaceCodes(cfg, "http")
}

// PreflightCodes returns sorted codes registered for GET /v1/preflight results.
func PreflightCodes(cfg *Config) []string {
	return surfaceCodes(cfg, "preflight")
}

func surfaceCodes(cfg *Config, surface string) []string {
	if cfg == nil {
		return nil
	}
	var out []string
	for code, entry := range cfg.UserNotices {
		if entry.IsUserVisible() && entry.HasSurface(surface) {
			out = append(out, code)
		}
	}
	sort.Strings(out)
	return out
}

func noticeCopyFromEntry(entry Entry) NoticeCopy {
	actions := append([]string(nil), entry.Actions...)
	if len(actions) == 0 && strings.TrimSpace(entry.Action) != "" {
		actions = []string{strings.TrimSpace(entry.Action)}
	}
	return NoticeCopy{
		Title:           entry.Title,
		Message:         entry.Message,
		SuggestedAction: entry.SuggestedAction,
		Actions:         actions,
	}
}

// validateActions checks every action a notice can render: a templated action
// is rendered for each scenario under every host-stamped context value.
func validateActions(code string, actions []string, scenarios []ScenarioEntry) error {
	for _, a := range actions {
		if !strings.Contains(a, "{{") && !strings.Contains(a, "{%") {
			if err := validateAction(code, a); err != nil {
				return err
			}
			continue
		}
		for _, ctx := range actionRenderContexts(scenarios) {
			rendered, err := renderFieldTemplateChecked(a, ctx)
			if err != nil {
				return fmt.Errorf("user_notices.%s: action pongo render: %w", code, err)
			}
			if err := validateAction(code, rendered); err != nil {
				return err
			}
		}
	}
	return nil
}

func actionRenderContexts(scenarios []ScenarioEntry) []map[string]any {
	bases := []map[string]any{{}}
	for _, scenario := range scenarios {
		bases = append(bases, scenario.Vars)
	}
	out := bases
	for name, values := range hostContextDomains {
		expanded := append([]map[string]any(nil), out...)
		for _, base := range out {
			for _, value := range values {
				ctx := make(map[string]any, len(base)+1)
				for k, v := range base {
					ctx[k] = v
				}
				ctx[name] = value
				expanded = append(expanded, ctx)
			}
		}
		out = expanded
	}
	return out
}

func validateAction(code, action string) error {
	action = strings.TrimSpace(action)
	if action == "" {
		return nil
	}
	if _, ok := allowedActions[action]; !ok {
		return fmt.Errorf("user_notices.%q: unknown action %q", code, action)
	}
	return nil
}

func requireNoticeCopy(label string, copy NoticeCopy) error {
	if strings.TrimSpace(copy.Title) == "" {
		return fmt.Errorf("%s: title is required", label)
	}
	if strings.TrimSpace(copy.Message) == "" {
		return fmt.Errorf("%s: message is required", label)
	}
	return nil
}

func isErrorCodeShape(code string) bool {
	if code == "" {
		return false
	}
	// Lowercase snake_case (HTTP catch-alls) or SCREAMING_SNAKE (attachment codes).
	first := rune(code[0])
	lower := first >= 'a' && first <= 'z'
	upper := first >= 'A' && first <= 'Z'
	if !lower && !upper {
		return false
	}
	for i, r := range code {
		if i == 0 {
			continue
		}
		if lower {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
				continue
			}
			return false
		}
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			continue
		}
		return false
	}
	return !strings.HasSuffix(code, "_") && !strings.Contains(code, "__")
}
