package integration

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/scan"
	scancfg "github.com/lycaon/lycaon/internal/scan/configuration"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestScanGuidancePrependDoesNotPersistMessages(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")

	projectDir := t.TempDir()
	scanStore := scan.NewSQLStore(sqlDB)
	coord := newTestCoordinator(t, scanStore, nil)
	_ = seedScanWithGuidance(t, scanStore, "dep-1", []api.ScanGuidanceSummary{
		{Code: "SCAN_SQL_INJECTION", Message: "fix sql", RuleID: "lycaon.ruby.sql-string-concat", Severity: "ERROR"},
	})

	provider := scan.NewGuidanceProvider(coord, scancfg.DefaultAgentBudget())
	provider.SetInjectRenderer(prompts.NewInjectRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	adapter := &scan.SessionGuidanceAdapter{
		Provider: provider,
		DelegationBySession: func(string) (string, string, bool) {
			return "dep-1", "", true
		},
	}

	sessStore := store.NewMemory()
	mgr := session.NewHost(sessStore, session.Models{Client: llm.NewMockProvider(nil), Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	loader, err := oar.NewLoader(filepath.Join(configlayout.FindModuleRoot(), "..", "schemas"))
	testutil.FailErr(t, "create policy loader", err)
	rules, err := loader.LoadEffectivePolicy()
	testutil.FailErr(t, "load stock policy", err)
	pipeline := oar.NewGuardPipeline(rules, loader, oar.NewCounterStore())
	pipeline.EnableAnchor(oar.AnchorCoordinatorCloseoutCheck)
	mgr.SetOARPipeline(pipeline, oar.NewRenderer(nil, nil))
	mgr.SetScanGuidance(adapter)

	ctx := context.Background()
	sess, err := mgr.Chats.CreateForProject(ctx, projectDir, api.SessionPostureBuild)
	testutil.FailErr(t, "mgr.Create failed", err)

	history := []api.Message{{Role: api.MessageRoleUser, Content: "next"}}
	out := adapter.PrependGuidance(ctx, sess.ID, history)
	if len(out) != len(history)+1 {
		t.Fatalf("expected prepended message, got %d", len(out))
	}
	if out[0].Role != api.MessageRoleSystem {
		t.Fatalf("role = %q", out[0].Role)
	}

	after, err := mgr.Runner.Transcript.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "mgr.GetMessages failed", err)
	if len(after) != 0 {
		t.Fatalf("message store must stay empty before persist, got %d", len(after))
	}

	if _, err := mgr.Submissions.Prompt(ctx, sess.ID, "hello"); err != nil {
		testutil.FailErr(t, "mgr.Submissions.Prompt failed", err)
	}
	msgs, err := mgr.Runner.Transcript.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "mgr.GetMessages failed", err)
	for _, msg := range msgs {
		if msg.Role == api.MessageRoleSystem {
			t.Fatal("ephemeral scan guidance must not be appended to message store")
		}
	}
}
