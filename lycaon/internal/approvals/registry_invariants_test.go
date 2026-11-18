package approvals

import (
	"testing"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRegistryRequiresCatalogFallback(t *testing.T) {
	cfg, err := LoadConfigStock()
	testutil.FailErr(t, "load explanation catalog", err)
	delete(cfg.Explanations, FallbackExplanationKey)
	if registry, err := newRegistry(cfg); err == nil || registry != nil {
		t.Fatal("registry accepted a missing fallback entry")
	}
}

func TestUnknownActionsUseCatalogCopy(t *testing.T) {
	cfg, err := LoadConfigStock()
	testutil.FailErr(t, "load explanation catalog", err)
	entry := cfg.Explanations[FallbackExplanationKey]
	entry.WhatChanges = "catalog-marker {{ tool }}"
	cfg.Explanations[FallbackExplanationKey] = entry
	registry, err := newRegistry(cfg)
	testutil.FailErr(t, "construct explanation registry", err)
	action := hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{
			Tool: "unknown_fixture_action",
		},
	}
	got := registry.ExplainAction(action, settings.TierIrreversible)
	want := RenderEntry(entry, ActionTemplateVars(action))
	if !got.UsedFallback || got.Copy != want {
		t.Fatalf("unknown action bypassed catalog copy: got=%+v want=%+v", got, want)
	}
}

// A known action renders its own catalog entry, never the fallback.
func TestKnownActionsUseTheirCatalogEntry(t *testing.T) {
	cfg, err := LoadConfigStock()
	testutil.FailErr(t, "load explanation catalog", err)
	entry := cfg.Explanations[KeyCommandDestructive]
	entry.WhatChanges = "destructive-what {{ tool }}"
	entry.IfWrong = "destructive-if-wrong"
	cfg.Explanations[KeyCommandDestructive] = entry
	registry, err := newRegistry(cfg)
	testutil.FailErr(t, "construct explanation registry", err)
	got := registry.ExplainAction(hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{
			Tool: "command",
			Args: map[string]any{"command": "git push origin main"},
		},
		Scope: hitl.ActionScope{ProjectDir: "/proj"},
	}, settings.TierIrreversible)
	if got.Key != KeyCommandDestructive || got.UsedFallback {
		t.Fatalf("destructive command explanation = key %q fallback %v", got.Key, got.UsedFallback)
	}
	if got.Copy.What != "destructive-what command" || got.Copy.IfWrong != "destructive-if-wrong" {
		t.Fatalf("destructive command copy = %+v, want the catalog entry", got.Copy)
	}
}
