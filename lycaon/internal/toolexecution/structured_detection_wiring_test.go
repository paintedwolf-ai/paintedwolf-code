package toolexecution

import (
	"context"
	"github.com/lycaon/lycaon/internal/tools"
	"testing"

	"github.com/lycaon/lycaon/internal/detectionpack"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

type capturePolicy struct {
	eval platform.PolicyContext
}

func (p *capturePolicy) Evaluate(_ context.Context, eval platform.PolicyContext) (*platform.PolicyDecision, error) {
	p.eval = eval
	return &platform.PolicyDecision{Allowed: true}, nil
}

func TestStructuredRegistryIdentityReachesProductionDetection(t *testing.T) {
	t.Parallel()
	registry := tools.NewDefaultRegistry()
	const tool = "mcp_payments_create_charge"
	testutil.FailErr(t, "register tool", registry.RegisterDefinition(tools.Definition{
		Contract: toolcontract.External("test"),
		Handler: func(context.Context, map[string]any, tools.ToolContext) (string, error) {
			return "ok", nil
		},
		Meta: tools.ToolMeta{
			Name: tool, Description: "charge", ArgsSchema: map[string]any{"type": "object"},
			ApprovalCategory: "mcp", ApprovalSubject: "payments.create_charge",
		},
	}))

	policy := &capturePolicy{}
	executor := NewExecutor(policy, registry, "implement")
	projectDir := t.TempDir()
	_, err := executor.Invoke(context.Background(), tool, map[string]any{
		"amount": 2500, "customer_id": "cus_123",
	}, tools.ToolContext{
		Roots:        []projectroot.RootRef{{ID: "root", Label: "root", Path: projectDir, IsPrimary: true}},
		ActiveRootID: "root",
		SessionID:    "session",
		ToolCallID:   "action",
	})
	testutil.FailErr(t, "Invoke", err)
	if policy.eval.ApprovalCategory != "mcp" || policy.eval.ApprovalSubject != "payments.create_charge" {
		t.Fatalf("policy identity=(%q,%q)", policy.eval.ApprovalCategory, policy.eval.ApprovalSubject)
	}

	catalog := loadShippedDetectionCatalog(t)
	semantics, err := detectionpack.LoadActionSemantics("")
	testutil.FailErr(t, "LoadActionSemantics", err)
	source := detectionpack.NewGateSource(detectionpack.NewMatcher(catalog), semantics)
	hit, ok := source.MatchAction(proposedActionFromPolicy(policy.eval), "balanced")
	if !ok || hit.PackID != "structured-action-outcomes" || hit.RuleTitle != "Execute a financial action" {
		t.Fatalf("structured detection=%+v ok=%v", hit, ok)
	}
}

func TestNativeTargetFilesReachProductionDetection(t *testing.T) {
	catalog := loadShippedDetectionCatalog(t)
	semantics, err := detectionpack.LoadActionSemantics("")
	testutil.FailErr(t, "LoadActionSemantics", err)
	source := detectionpack.NewGateSource(detectionpack.NewMatcher(catalog), semantics)

	cases := []struct {
		name  string
		tool  string
		args  map[string]any
		title string
	}{
		{"git restore", "git_restore", map[string]any{"paths": []string{"src/main.go"}}, "Discard local changes with Git restore"},
		{"ssh access", "write", map[string]any{"path": "~/.ssh/authorized_keys", "content": "fixture"}, "Change SSH authorized keys"},
		{"shell startup", "edit", map[string]any{"path": "~/.zshrc", "old_string": "before", "new_string": "after"}, "Change a shell startup file"},
		{"service definition", "write", map[string]any{"path": "~/Library/LaunchAgents/example.plist", "content": "fixture"}, "Change an automatic service definition"},
		{"security config", "edit", map[string]any{"path": "/etc/ssh/sshd_config", "old_string": "before", "new_string": "after"}, "Change a host access-control configuration"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			action := proposedActionFromPolicy(platform.PolicyContext{
				ToolName: tc.tool, ToolArgs: tc.args, ProjectDir: t.TempDir(),
				SessionID: "session", ActionID: "action",
			})
			if len(action.Files) == 0 {
				t.Fatal("policy adapter projected no target files")
			}
			hit, ok := source.MatchAction(action, "balanced")
			if !ok || hit.PackID != "structured-action-outcomes" || hit.RuleTitle != tc.title {
				t.Fatalf("native detection=%+v ok=%v", hit, ok)
			}
		})
	}

	read := proposedActionFromPolicy(platform.PolicyContext{
		ToolName: "read", ToolArgs: map[string]any{"path": "~/.ssh/authorized_keys"},
		ProjectDir: t.TempDir(), SessionID: "session", ActionID: "read-action",
	})
	if hit, ok := source.MatchAction(read, "light"); ok {
		t.Fatalf("read-only sensitive path matched %+v", hit)
	}
}

func loadShippedDetectionCatalog(t *testing.T) *detectionpack.Catalog {
	t.Helper()
	effective, err := extpacks.ResolveStockCatalog(t.Context(), nil)
	testutil.FailErr(t, "resolve shipped packs", err)
	packs, diagnostics := extpacks.LoadEffectiveDetectionPacks(effective)
	if len(diagnostics) != 0 {
		t.Fatalf("load shipped packs: %+v", diagnostics)
	}
	catalog, err := detectionpack.LoadCatalog(detectionpack.Input{Contributed: packs})
	testutil.FailErr(t, "load detection catalog", err)
	return catalog
}
