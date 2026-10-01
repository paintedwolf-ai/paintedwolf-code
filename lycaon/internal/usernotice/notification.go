package usernotice

import (
	"fmt"
	"sort"
	"strings"
)

// Notice tiers describe whether the application remains usable.
const (
	// TierCatastrophic blocks application startup.
	TierCatastrophic = "catastrophic"
	// TierNonCatastrophic renders at one scope while the app keeps working.
	TierNonCatastrophic = "non_catastrophic"
)

// Notice scopes identify the rendering boundary.
const (
	ScopeApp     = "app"
	ScopeProject = "project"
	ScopeSession = "session"
)

// DefaultResolutionID names the single resolution of an unconditional notice.
const DefaultResolutionID = "default"

var allowedTiers = map[string]struct{}{
	TierCatastrophic:    {},
	TierNonCatastrophic: {},
}

var allowedScopes = map[string]struct{}{
	ScopeApp:     {},
	ScopeProject: {},
	ScopeSession: {},
}

// sessionScopeSurfaces can carry session notices.
var sessionScopeSurfaces = map[string]struct{}{
	"host_error":     {},
	"http":           {},
	"worker_failure": {},
}

// Resolution maps a discriminator value to placement.
type Resolution struct {
	ID    string `yaml:"id"`
	When  string `yaml:"when"`
	Tier  string `yaml:"tier"`
	Scope string `yaml:"scope"`
}

// IsFallback reports whether this resolution applies when no `when` matched.
func (r Resolution) IsFallback() bool {
	return strings.TrimSpace(r.When) == ""
}

// Notification declares notice placement.
type Notification struct {
	// Sugar form.
	Tier  string `yaml:"tier"`
	Scope string `yaml:"scope"`

	// Conditional form uses exact string equality.
	Discriminator string       `yaml:"discriminator"`
	Resolutions   []Resolution `yaml:"resolutions"`
}

// IsConditional reports whether the block uses the resolution list form.
func (n Notification) IsConditional() bool {
	return len(n.Resolutions) > 0
}

// Normalized returns one resolution shape.
func (n Notification) Normalized() []Resolution {
	if n.IsConditional() {
		out := make([]Resolution, len(n.Resolutions))
		copy(out, n.Resolutions)
		return out
	}
	return []Resolution{{ID: DefaultResolutionID, Tier: n.Tier, Scope: n.Scope}}
}

// ResolutionIDs returns the sorted resolution ids.
func (n Notification) ResolutionIDs() []string {
	res := n.Normalized()
	out := make([]string, 0, len(res))
	for _, r := range res {
		out = append(out, r.ID)
	}
	sort.Strings(out)
	return out
}

// Resolve picks an exact match or its fallback.
func (n Notification) Resolve(ctx map[string]any) (Resolution, bool) {
	res := n.Normalized()
	if !n.IsConditional() {
		return res[0], true
	}
	got, ok := discriminatorValue(ctx, n.Discriminator)
	if ok {
		for _, r := range res {
			if !r.IsFallback() && r.When == got {
				return r, true
			}
		}
	}
	for _, r := range res {
		if r.IsFallback() {
			return r, true
		}
	}
	return Resolution{}, false
}

// ResolutionByID looks up one resolution by id.
func (n Notification) ResolutionByID(id string) (Resolution, bool) {
	for _, r := range n.Normalized() {
		if r.ID == id {
			return r, true
		}
	}
	return Resolution{}, false
}

// discriminatorValue reads one string context key.
func discriminatorValue(ctx map[string]any, key string) (string, bool) {
	if ctx == nil || strings.TrimSpace(key) == "" {
		return "", false
	}
	raw, ok := ctx[key]
	if !ok {
		return "", false
	}
	s, ok := raw.(string)
	if !ok {
		return "", false
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return "", false
	}
	return s, true
}

// validateNotification enforces the tier/scope declaration rules for one entry.
func validateNotification(code string, entry Entry) error {
	label := "user_notices." + code
	if !entry.IsUserVisible() {
		if entry.Notification != nil {
			return fmt.Errorf("%s: user_visible:false must not declare notification", label)
		}
		return nil
	}
	if entry.Notification == nil {
		return fmt.Errorf("%s: visible entries must declare notification (tier + scope)", label)
	}
	n := *entry.Notification

	if n.IsConditional() {
		if strings.TrimSpace(n.Tier) != "" || strings.TrimSpace(n.Scope) != "" {
			return fmt.Errorf("%s: notification with resolutions must not also set tier/scope", label)
		}
		if strings.TrimSpace(n.Discriminator) == "" {
			return fmt.Errorf("%s: notification with resolutions must set discriminator", label)
		}
		if !contextSchemaDocuments(entry.ContextSchema, n.Discriminator) {
			return fmt.Errorf("%s: notification discriminator %q must be documented in context_schema", label, n.Discriminator)
		}
		if len(n.Resolutions) < 2 {
			return fmt.Errorf("%s: notification resolutions must list at least two (use tier/scope for one)", label)
		}
	} else if strings.TrimSpace(n.Discriminator) != "" {
		return fmt.Errorf("%s: notification discriminator requires resolutions", label)
	}

	seenID := map[string]struct{}{}
	seenWhen := map[string]struct{}{}
	fallbacks := 0
	for _, r := range n.Normalized() {
		if strings.TrimSpace(r.ID) == "" {
			return fmt.Errorf("%s: notification resolution requires id", label)
		}
		if _, dup := seenID[r.ID]; dup {
			return fmt.Errorf("%s: duplicate notification resolution id %q", label, r.ID)
		}
		seenID[r.ID] = struct{}{}
		if r.IsFallback() {
			fallbacks++
		} else {
			if _, dup := seenWhen[r.When]; dup {
				return fmt.Errorf("%s: duplicate notification resolution when %q", label, r.When)
			}
			seenWhen[r.When] = struct{}{}
		}
		if err := validateResolutionTierScope(label, r, entry); err != nil {
			return err
		}
	}
	if fallbacks > 1 {
		return fmt.Errorf("%s: notification must declare at most one fallback resolution", label)
	}
	return validateScenarioResolutions(label, n, entry.Scenarios)
}

func validateResolutionTierScope(label string, r Resolution, entry Entry) error {
	if _, ok := allowedTiers[r.Tier]; !ok {
		return fmt.Errorf("%s: resolution %q has unknown tier %q", label, r.ID, r.Tier)
	}
	if _, ok := allowedScopes[r.Scope]; !ok {
		return fmt.Errorf("%s: resolution %q has unknown scope %q", label, r.ID, r.Scope)
	}
	if r.Tier == TierCatastrophic {
		// Startup blockers render on the preflight application surface.
		if r.Scope != ScopeApp {
			return fmt.Errorf("%s: resolution %q is catastrophic so scope must be %q", label, r.ID, ScopeApp)
		}
		if len(entry.Surfaces) != 1 || entry.Surfaces[0] != "preflight" {
			return fmt.Errorf("%s: resolution %q is catastrophic so surfaces must be exactly [preflight]", label, r.ID)
		}
	}
	if r.Scope == ScopeSession {
		for _, s := range entry.Surfaces {
			if _, ok := sessionScopeSurfaces[s]; !ok {
				return fmt.Errorf("%s: resolution %q is session-scoped so surface %q is not deliverable", label, r.ID, s)
			}
		}
	}
	return nil
}

// validateScenarioResolutions requires fixtures for every branch.
func validateScenarioResolutions(label string, n Notification, scenarios []ScenarioEntry) error {
	if !n.IsConditional() {
		for _, s := range scenarios {
			if id := strings.TrimSpace(s.Resolution); id != "" && id != DefaultResolutionID {
				return fmt.Errorf("%s: scenario %q names resolution %q but the notification has none", label, s.ID, id)
			}
		}
		return nil
	}
	covered := map[string]struct{}{}
	for _, s := range scenarios {
		id := strings.TrimSpace(s.Resolution)
		if id == "" {
			return fmt.Errorf("%s: scenario %q must name a resolution", label, s.ID)
		}
		if _, ok := n.ResolutionByID(id); !ok {
			return fmt.Errorf("%s: scenario %q names unknown resolution %q", label, s.ID, id)
		}
		covered[id] = struct{}{}
	}
	for _, id := range n.ResolutionIDs() {
		if _, ok := covered[id]; !ok {
			return fmt.Errorf("%s: resolution %q has no scenario", label, id)
		}
	}
	return nil
}

// contextSchemaDocuments reports whether key appears in the entry's
// context_schema required/optional lists.
func contextSchemaDocuments(schema map[string]any, key string) bool {
	key = strings.TrimSpace(key)
	if schema == nil || key == "" {
		return false
	}
	for _, group := range []string{"required", "optional"} {
		raw, ok := schema[group]
		if !ok {
			continue
		}
		list, ok := raw.([]any)
		if !ok {
			continue
		}
		for _, item := range list {
			if s, ok := item.(string); ok && strings.TrimSpace(s) == key {
				return true
			}
		}
	}
	return false
}
