package app

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/hostresources"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
)

type hostResourceApprovalRules struct {
	layers settings.ApprovalRuleLayers
}

func (s hostResourceApprovalRules) RuleLayers(context.Context, string) settings.ApprovalRuleLayers {
	return s.layers
}

func TestHostResourcePolicyIncludesExtensionRules(t *testing.T) {
	store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "approvals.yaml"))
	testutil.FailErr(t, "new approval store", err)
	testutil.FailErr(t, "set editable host-resource rule", store.SetHostResourceRule(
		llm.SettingsScopeGlobal, "", "local-db", settings.ApprovalEffectAsk,
	))

	source := hostResourceApprovalRules{layers: settings.ApprovalRuleLayers{
		Project: []settings.ApprovalRule{{
			Category: settings.ApprovalCategoryHostResource,
			Pattern:  "local-db",
			Effect:   settings.ApprovalEffectDeny,
		}},
	}}
	policy := newHostResourcePolicyBinder(store, source)
	decide := policy(context.Background(), hostresources.ProjectContext{ID: "project-1", Dir: t.TempDir()})
	decision := decide("local-db", "data")

	if decision.Access != hostresources.AccessDeny {
		t.Fatalf("effective access = %q, want deny", decision.Access)
	}
	if decision.Setting != hostresources.AccessSettingAsk {
		t.Fatalf("editable setting = %q, want ask", decision.Setting)
	}
}
