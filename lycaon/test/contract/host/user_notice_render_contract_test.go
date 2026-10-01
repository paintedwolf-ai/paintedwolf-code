package contract

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/usernotice"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestUserNoticeScenarioFixturesRender(t *testing.T) {
	t.Parallel()
	cfg := loadUserNoticeConfig(t)
	for code, entry := range cfg.UserNotices {
		for _, sc := range entry.Scenarios {
			copy := usernotice.RenderCopy(entry, sc.Vars)
			for _, want := range sc.ExpectContains {
				blob := strings.Join([]string{copy.Title, copy.Message, copy.SuggestedAction}, "\n")
				if !strings.Contains(blob, want) {
					t.Fatalf("%s scenario %q missing %q in %q", code, sc.ID, want, blob)
				}
			}
		}
	}
}

// TestUserNoticeVisibleEntriesRenderClean covers every conditional fixture branch.
func TestUserNoticeVisibleEntriesRenderClean(t *testing.T) {
	t.Parallel()
	cfg := loadUserNoticeConfig(t)
	catalog := usernotice.NewCatalog(cfg)

	for code, entry := range cfg.UserNotices {
		if !entry.IsUserVisible() {
			continue
		}
		for _, ctx := range scenarioVarSetsForEntry(entry) {
			copy := catalog.RenderWire(code, ctx.vars)
			blob := strings.Join([]string{copy.Title, copy.Message, copy.SuggestedAction}, "\n")
			if strings.TrimSpace(copy.Title) == "" || strings.TrimSpace(copy.Message) == "" {
				t.Fatalf("%s scenario %q: RenderWire produced empty title/message: %#v", code, ctx.id, copy)
			}
			if strings.TrimSpace(copy.SuggestedAction) == "" {
				t.Fatalf("%s scenario %q: RenderWire produced empty suggested_action", code, ctx.id)
			}
			if strings.Contains(blob, "{{") || strings.Contains(blob, "{%") {
				t.Fatalf("%s scenario %q: unrendered template in copy: %q", code, ctx.id, blob)
			}
			// Empty vars are caught by each scenario's expect_contains
			// (TestUserNoticeScenarioFixturesRender); prose shape checks here
			// misfire on text such as ".gitignore".
		}
	}
}

func TestUserNoticeConditionalEntriesRequireScenarios(t *testing.T) {
	cfg := loadUserNoticeConfig(t)
	var violations []string
	for code, entry := range cfg.UserNotices {
		if entry.UsesDefaults() || !entry.IsUserVisible() {
			continue
		}
		if !entryHasConditionalTemplate(entry) {
			continue
		}
		if len(entry.Scenarios) == 0 {
			violations = append(violations, code+": conditional template requires at least one scenarios fixture")
		}
	}
	contractcheck.FailViolations(t, "user notice conditional copy missing scenarios", violations)
}

func TestUserNoticeContextSchemaDocumented(t *testing.T) {
	cfg := loadUserNoticeConfig(t)
	promptFixtures := contextFromPromptErrorFixtures()
	preflightFixtures := preflightDetailFixtures(t)

	var violations []string
	for code, entry := range cfg.UserNotices {
		keys := contextSchemaKeys(entry)
		if len(keys) == 0 {
			continue
		}
		// Which producer supplies the vars follows from the delivery surface:
		// readiness copy is fed by probe Detail, HTTP-only copy by handler error
		// details (TestAPIHandlersEmitOnlyDeclaredErrors/details), everything else
		// by ContextFromPromptError.
		if len(entry.Surfaces) == 1 && entry.HasSurface("http") {
			continue
		}
		fixture, ok := preflightFixtures[code]
		producer := "probe Detail"
		if !entry.HasSurface("preflight") {
			fixture, ok = promptFixtures[code]
			producer = "ContextFromPromptError"
		}
		if !ok {
			violations = append(violations, code+": context_schema declared but no "+producer+" fixture in contract test")
			continue
		}
		for _, key := range keys {
			if _, ok := fixture[key]; !ok {
				violations = append(violations, code+": context_schema key "+key+" never emitted by "+producer+" fixture")
			}
		}
	}
	contractcheck.FailViolations(t, "user notice context_schema drift", violations)
}

func TestUserNoticeDefaultsRender(t *testing.T) {
	cfg := loadUserNoticeConfig(t)
	catalog := usernotice.NewCatalog(cfg)
	def := catalog.RenderWire("totally_unknown_code", nil)
	if def.Title != cfg.Defaults.Title {
		t.Fatalf("defaults title = %q want %q", def.Title, cfg.Defaults.Title)
	}
}

func TestUserNoticeUseDefaultsEntriesRenderDefaults(t *testing.T) {
	cfg := loadUserNoticeConfig(t)
	catalog := usernotice.NewCatalog(cfg)
	for code, entry := range cfg.UserNotices {
		if !entry.UsesDefaults() {
			continue
		}
		got := catalog.RenderWire(code, nil)
		want := catalog.RenderWire("totally_unknown_code", nil)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s use_defaults RenderWire = %#v want defaults %#v", code, got, want)
		}
	}
}

func entryHasConditionalTemplate(entry usernotice.Entry) bool {
	blob := strings.Join([]string{entry.Title, entry.Message, entry.SuggestedAction}, "\n")
	return strings.Contains(blob, "{% if") || strings.Contains(blob, "{%- if")
}

type scenarioVarSet struct {
	id   string
	vars map[string]any
}

// scenarioVarSetsForEntry returns one var set per declared fixture, so callers
// exercise every branch a conditional template can take. An entry with no
// fixtures still yields one empty set, which is the "no vars" case.
func scenarioVarSetsForEntry(entry usernotice.Entry) []scenarioVarSet {
	if len(entry.Scenarios) == 0 {
		return []scenarioVarSet{{id: "(none)"}}
	}
	out := make([]scenarioVarSet, 0, len(entry.Scenarios))
	for _, sc := range entry.Scenarios {
		out = append(out, scenarioVarSet{id: sc.ID, vars: sc.Vars})
	}
	return out
}

// contextSchemaKeys returns every documented context key. Required keys count
// too: a discriminator is required, and an unsupplied discriminator would leave
// the notice with no resolvable tier.
func contextSchemaKeys(entry usernotice.Entry) []string {
	if entry.ContextSchema == nil {
		return nil
	}
	var out []string
	for _, group := range []string{"required", "optional"} {
		raw, ok := entry.ContextSchema[group]
		if !ok {
			continue
		}
		list, ok := raw.([]any)
		if !ok {
			continue
		}
		for _, item := range list {
			s, ok := item.(string)
			if ok && strings.TrimSpace(s) != "" {
				out = append(out, s)
			}
		}
	}
	sort.Strings(out)
	return out
}
