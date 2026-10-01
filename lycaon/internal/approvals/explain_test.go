package approvals_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/approvals"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestExplainActionCommandDestructive(t *testing.T) {
	reg, err := approvals.LoadRegistryStock()
	if err != nil {
		t.Fatalf("LoadRegistryStock: %v", err)
	}
	got := reg.ExplainAction(hitl.ProposedAction{
		Tool: "command",
		Args: map[string]any{"command": "git push origin main"},
	}, settings.TierIrreversible)
	if got.Key != approvals.KeyCommandDestructive {
		t.Fatalf("key: got %q want %q", got.Key, approvals.KeyCommandDestructive)
	}
	if got.Copy.What == "" || got.Copy.IfWrong == "" {
		t.Fatalf("expected rendered copy: %+v", got.Copy)
	}
}

func TestExplainActionUnknownUsesFallback(t *testing.T) {
	reg, err := approvals.LoadRegistryStock()
	if err != nil {
		t.Fatalf("LoadRegistryStock: %v", err)
	}
	got := reg.ExplainAction(hitl.ProposedAction{Tool: "not_in_registry_xyz"}, settings.TierIrreversible)
	if !got.UsedFallback {
		t.Fatal("expected UsedFallback for unknown tool")
	}
	if got.Copy.What == "" {
		t.Fatal("expected generic what copy")
	}
}

func TestGateableKeysComplete(t *testing.T) {
	reg, err := approvals.LoadRegistryStock()
	if err != nil {
		t.Fatalf("LoadRegistryStock: %v", err)
	}
	for _, key := range approvals.GateableKeys() {
		if _, ok := reg.Entry(key); !ok {
			t.Fatalf("missing gateable entry %q", key)
		}
	}
	if len(reg.Keys()) == 0 {
		t.Fatal("expected registry keys")
	}
}

func TestWriteConfigDirRoundTrip(t *testing.T) {
	cfg, err := approvals.LoadConfigStock()
	testutil.FailErr(t, "LoadConfigStock", err)
	dir := t.TempDir()
	testutil.FailErr(t, "WriteConfigDir", approvals.WriteConfigDir(dir, cfg))
	round, err := approvals.LoadConfig(dir)
	testutil.FailErr(t, "LoadConfig round trip", err)
	if len(round.Explanations) != len(cfg.Explanations) {
		t.Fatalf("round trip count = %d want %d", len(round.Explanations), len(cfg.Explanations))
	}
}

func TestRenderFailureNeverReturnsAuthoredExplanationTemplate(t *testing.T) {
	raw := "{{ invalid"
	copy := approvals.RenderEntry(approvals.ExplanationEntry{WhatChanges: raw}, nil)
	if copy.What == raw {
		t.Fatalf("render failure exposed template source: %q", copy.What)
	}
}

func TestMCPExplanationUsesHostIdentity(t *testing.T) {
	reg, err := approvals.LoadRegistryStock()
	testutil.FailErr(t, "load explanation registry", err)
	for _, subject := range []string{"Provider with spaces.search", "Provider.with.dots.tool.with.dots", "Provider[ab].read"} {
		t.Run(subject, func(t *testing.T) {
			action := hitl.ProposedAction{
				Tool: "mcp_provider_search", ApprovalCategory: "mcp", ApprovalSubject: subject,
				Args: map[string]any{"provider": "spoof-provider", "action_label": "spoof-action"},
			}
			vars := approvals.ActionTemplateVars(action)
			if vars["action_label"] != subject || vars["tool"] != action.Tool {
				t.Fatalf("display altered tool identity: %+v", vars)
			}
			got := reg.ExplainAction(action, settings.TierRecoverable)
			if got.Key != approvals.KeyMCPCall || got.UsedFallback || !strings.Contains(got.Copy.What, subject) {
				t.Fatalf("MCP explanation lost canonical identity: %+v", got)
			}
			if strings.Contains(got.Copy.What, "spoof-") {
				t.Fatalf("MCP arguments replaced host identity: %q", got.Copy.What)
			}
		})
	}
}
