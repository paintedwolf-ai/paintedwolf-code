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
