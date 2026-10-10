package toolhost_test

import (
	"context"
	"encoding/json"
	"github.com/lycaon/lycaon/internal/configlayout"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/authzcontext"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/egressproxy"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/extpackstest"
	"github.com/lycaon/lycaon/internal/toolhost"
)

type extensionRuleSource struct{}

func (extensionRuleSource) RuleLayers(context.Context, string) settings.ApprovalRuleLayers {
	return settings.ApprovalRuleLayers{Project: []settings.ApprovalRule{{
		Category: settings.ApprovalCategoryHost, Pattern: "managed.blocked.test", Effect: settings.ApprovalEffectDeny,
		Source: settings.ApprovalRuleSource{
			UnitID: "approvals/rules/managed-host", PackID: "acme/policy", Scope: settings.ApprovalRuleScopeProject,
		},
	}}}
}

func TestEgressUsesEffectiveProjectApprovalConfig(t *testing.T) {
	root := configlayout.FindModuleRoot()
	store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "approvals.yaml"))
	testutil.FailErr(t, "settings.NewApprovalStoreAt failed", err)
	neverAsk := true
	err = store.PutGlobal(settings.ApprovalConfig{
		Posture:  gate.PostureBalanced,
		NeverAsk: &neverAsk,
	})
	testutil.FailErr(t, "store.PutGlobal failed", err)
	projectDir := t.TempDir()
	restoreAsks := false
	err = store.PutProject(projectDir, settings.ApprovalConfig{
		Posture:  gate.PostureStrict,
		NeverAsk: &restoreAsks,
		Rules: []settings.ApprovalRule{{
			Category: settings.ApprovalCategoryHost,
			Pattern:  "blocked.project.test",
			Effect:   settings.ApprovalEffectDeny,
		}},
	})
	testutil.FailErr(t, "store.PutProject failed", err)

	runtime, err := toolhost.NewRuntime(toolhost.RuntimeConfig{ConfigRoot: root, Approvals: store, Catalog: extpackstest.StockCatalog(t)})
	testutil.FailErr(t, "toolhost.NewRuntime failed", err)
	runtime.Authority.SetApprovalRuleSource(extensionRuleSource{})
	authzStore := authzcontext.NewMemoryStore()
	runtime.Authority.SetAuthzRecorder(authzcontext.LedgerRecorder{Ledger: &authzcontext.Ledger{Store: authzStore}})
	t.Cleanup(func() {
		confine.SetEgressRuleEvaluator(nil)
		confine.SetEgressPostureResolver(nil)
		confine.SetApprovalsDisabledSource(nil)
	})
	if !runtime.Authority.ApprovalsDisabled("") {
		t.Fatal("global never-ask was not effective")
	}
	if runtime.Authority.ApprovalsDisabled(projectDir) {
		t.Fatal("project did not restore its tighter approval layer")
	}

	var asks atomic.Int32
	confine.SetEgressResolver(func(context.Context, confine.EgressCommand, egressproxy.Endpoint, *confine.EgressDetectionCitation) bool {
		asks.Add(1)
		return true
	})
	t.Cleanup(func() { confine.SetEgressResolver(nil) })

	if !confine.DecideAttributedHost(t.Context(), confine.EgressCommand{
		SessionID: "global-balanced",
	}, "content.global.test") {
		t.Fatal("global Balanced host was denied")
	}
	if asks.Load() != 0 {
		t.Fatalf("global Balanced asks = %d, want 0", asks.Load())
	}

	projectCmd := confine.EgressCommand{SessionID: "project-strict", ProjectDir: projectDir}
	if !confine.DecideAttributedHost(t.Context(), projectCmd, "content.project.test") {
		t.Fatal("approved project Strict host was denied")
	}
	if asks.Load() != 1 {
		t.Fatalf("project Strict asks = %d, want 1", asks.Load())
	}
	if confine.DecideAttributedHost(t.Context(), projectCmd, "managed.blocked.test") {
		t.Fatal("extension host deny rule was ignored")
	}
	rows, err := authzStore.ListEvents(t.Context(), projectCmd.SessionID)
	testutil.FailErr(t, "list extension deny events", err)
	if len(rows) != 1 {
		t.Fatalf("extension deny events = %d want 1", len(rows))
	}
	var detail authzcontext.EventDetail
	if err := json.Unmarshal([]byte(rows[0].DetailJSON), &detail); err != nil {
		testutil.FailErr(t, "decode extension deny detail", err)
	}
	if len(detail.ApprovalRules) != 1 || detail.ApprovalRules[0].UnitID != "approvals/rules/managed-host" {
		t.Fatalf("approval rule provenance = %#v", detail.ApprovalRules)
	}
	if confine.DecideAttributedHost(t.Context(), projectCmd, "blocked.project.test") {
		t.Fatal("project host deny rule was ignored")
	}
	if asks.Load() != 1 {
		t.Fatalf("project deny unexpectedly raised a card; asks = %d", asks.Load())
	}
}
