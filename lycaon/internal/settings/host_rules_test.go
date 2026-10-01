package settings_test

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/settings"
)

func TestEvaluateHostRule(t *testing.T) {
	rules := []settings.ApprovalRule{
		{Category: settings.ApprovalCategoryHost, Pattern: "github.com", Effect: settings.ApprovalEffectAsk},
		{Category: settings.ApprovalCategoryHost, Pattern: "*.npmjs.org", Effect: settings.ApprovalEffectAsk},
		{Category: settings.ApprovalCategoryHost, Pattern: "evil.example", Effect: settings.ApprovalEffectDeny},
		{Category: settings.ApprovalCategoryPath, Pattern: "src/**", Effect: settings.ApprovalEffectAsk}, // ignored
	}
	cases := map[string]struct {
		effect  settings.ApprovalEffect
		matched bool
	}{
		"github.com":         {settings.ApprovalEffectAsk, true},
		"registry.npmjs.org": {settings.ApprovalEffectAsk, true}, // subdomain wildcard
		"npmjs.org":          {settings.ApprovalEffectAsk, true}, // apex matches *.npmjs.org
		"evil.example":       {settings.ApprovalEffectDeny, true},
		"unlisted.example":   {"", false},
	}
	for host, want := range cases {
		rule, matched := settings.EvaluateHostRule(rules, host)
		if matched != want.matched || (matched && rule.Effect != want.effect) {
			t.Fatalf("host %q: got (%q, %v) want (%q, %v)", host, rule.Effect, matched, want.effect, want.matched)
		}
	}
}

// Overlapping host rules in one list resolve to the deny, whatever the order
// or breadth of its pattern.
func TestEvaluateHostRuleDenyWinsWithinList(t *testing.T) {
	deny := settings.ApprovalRule{Category: settings.ApprovalCategoryHost, Pattern: "*.example.com", Effect: settings.ApprovalEffectDeny}
	ask := settings.ApprovalRule{Category: settings.ApprovalCategoryHost, Pattern: "api.example.com", Effect: settings.ApprovalEffectAsk}
	for name, rules := range map[string][]settings.ApprovalRule{
		"deny first": {deny, ask},
		"ask first":  {ask, deny},
	} {
		rule, ok := settings.EvaluateHostRule(rules, "api.example.com")
		if !ok || rule.Effect != settings.ApprovalEffectDeny || rule.Pattern != "*.example.com" {
			t.Fatalf("%s: host rule = %#v matched=%v", name, rule, ok)
		}
	}
	// Among asks the most specific pattern is cited.
	broad := settings.ApprovalRule{Category: settings.ApprovalCategoryHost, Pattern: "*.example.com", Effect: settings.ApprovalEffectAsk}
	rule, ok := settings.EvaluateHostRule([]settings.ApprovalRule{ask, broad}, "api.example.com")
	if !ok || rule.Pattern != "api.example.com" {
		t.Fatalf("ask citation = %#v matched=%v", rule, ok)
	}
}

func TestEvaluateHostAndWriteRootLayersAreAdditive(t *testing.T) {
	hostLayers := settings.ApprovalRuleLayers{
		Device: []settings.ApprovalRule{{
			Category: settings.ApprovalCategoryHost, Pattern: "*.example.com", Effect: settings.ApprovalEffectDeny,
		}},
		Project: []settings.ApprovalRule{{
			Category: settings.ApprovalCategoryHost, Pattern: "api.example.com", Effect: settings.ApprovalEffectAsk,
		}},
	}
	if rule, ok := settings.EvaluateHostRuleLayers(hostLayers, "api.example.com"); !ok || rule.Effect != settings.ApprovalEffectDeny {
		t.Fatalf("host rule = %#v matched=%v", rule, ok)
	}

	root := filepath.Join(t.TempDir(), "cache")
	rootLayers := settings.ApprovalRuleLayers{
		Device: []settings.ApprovalRule{{
			Category: settings.ApprovalCategoryWriteRoot, Pattern: root, Effect: settings.ApprovalEffectDeny,
		}},
		Project: []settings.ApprovalRule{{
			Category: settings.ApprovalCategoryWriteRoot, Pattern: root, Effect: settings.ApprovalEffectAsk,
		}},
	}
	if rule, ok := settings.EvaluateWriteRootRuleLayers(rootLayers, root); !ok || rule.Effect != settings.ApprovalEffectDeny {
		t.Fatalf("write-root rule = %#v matched=%v", rule, ok)
	}
}

func TestEvaluateHostRulePreservesAuthoredPattern(t *testing.T) {
	rule, ok := settings.EvaluateHostRule([]settings.ApprovalRule{{
		Category: settings.ApprovalCategoryHost, Pattern: "*.example.com", Effect: settings.ApprovalEffectAsk,
	}}, "api.example.com")
	if !ok || rule.Pattern != "*.example.com" {
		t.Fatalf("matched rule = %+v/%v", rule, ok)
	}
}

func TestEvaluateWriteRootRuleReturnsAskAndDeny(t *testing.T) {
	rules := []settings.ApprovalRule{
		{Category: settings.ApprovalCategoryWriteRoot, Pattern: "/opt/cache", Effect: settings.ApprovalEffectAsk},
		{Category: settings.ApprovalCategoryWriteRoot, Pattern: "/srv/data", Effect: settings.ApprovalEffectDeny},
	}
	for root, effect := range map[string]settings.ApprovalEffect{
		"/opt/cache/": settings.ApprovalEffectAsk,
		"/srv/data":   settings.ApprovalEffectDeny,
	} {
		rule, ok := settings.EvaluateWriteRootRule(rules, root)
		if !ok || rule.Effect != effect {
			t.Fatalf("root %q = %+v/%v, want %q", root, rule, ok, effect)
		}
	}
	if _, ok := settings.EvaluateWriteRootRule(rules, "/opt/cache/subdir"); ok {
		t.Fatal("write-root rule must not silently widen to descendant roots")
	}
}
