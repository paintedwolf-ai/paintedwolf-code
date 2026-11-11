//go:build integration

package governance_test

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/assembly"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session/policyindex"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// integrationEngine renders against shipped prompts with no host overlay: the
// bundled layer is the binary, so an empty PromptLayers is the stock stack.
func integrationEngine(t *testing.T) *prompts.FileTemplateEngine {
	t.Helper()
	return prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
}

func TestCoordinatorTurnIncludesChainOnWritePath(t *testing.T) {
	root := agentsMDFixture(t, "monorepo")
	engine := integrationEngine(t)
	policies := policyindex.New(nil, integrationPolicyRoot{root})
	policies.SetRenderer(engine)
	deps := assembly.AssemblyDeps{
		Prompts:       engine,
		AgentsMDIndex: policies.Index,
		AgentsMDPolicies: func(ctx context.Context, sess *api.Session, history []api.Message) ([]api.Message, error) {
			delivery, err := policies.ResolveCurrent(ctx, sess, history)
			return delivery.Messages, err
		},
	}
	asm := &assembly.AssemblyEngine{}
	asm.SetDeps(deps)
	sess := &api.Session{ID: "sess-chain", ProjectID: "project", WorkspaceRootID: "root", WorkspacePath: root}
	history := []api.Message{
		{Role: api.MessageRoleUser, Content: "edit backend"},
		{Role: api.MessageRoleTool, Origin: api.MessageOriginTool,
			SourceContext: &api.SourceContext{Locations: []api.NavigationTarget{{ProjectID: "project", RootID: "root", Path: "lycaon/foo.go", EntryKind: api.NavigationEntryKindFile}}},
			ToolResult:    &api.ToolResult{Tool: "command", ToolCallID: "observed", Invocation: &api.InvocationReceipt{Tool: "command", ToolCallID: "observed"}},
		},
	}
	msgs, err := asm.BuildCompletionMessages(context.Background(), sess, history, nil)
	testutil.FailErr(t, "BuildCompletionMessages", err)
	combined := strings.Builder{}
	for _, msg := range msgs {
		if msg.Role == api.MessageRoleSystem {
			combined.WriteString(msg.Content)
			combined.WriteByte('\n')
		}
	}
	out := combined.String()
	if !strings.Contains(out, "Available AGENTS.md files") {
		t.Fatalf("missing index inject:\n%s", out)
	}
	if !strings.Contains(out, "Go backend policy") {
		t.Fatalf("missing chain inject:\n%s", out)
	}
}

func TestCoordinatorTurnIncludesRootChainBeforeAnyPathTool(t *testing.T) {
	root := agentsMDFixture(t, "monorepo")
	engine := integrationEngine(t)
	policies := policyindex.New(nil, integrationPolicyRoot{root})
	policies.SetRenderer(engine)
	asm := &assembly.AssemblyEngine{}
	asm.SetDeps(assembly.AssemblyDeps{
		Prompts: engine,
		AgentsMDPolicies: func(ctx context.Context, sess *api.Session, history []api.Message) ([]api.Message, error) {
			delivery, err := policies.ResolveCurrent(ctx, sess, history)
			return delivery.Messages, err
		},
	})
	sess := &api.Session{ID: "sess-root", ProjectID: "project", WorkspaceRootID: "root", WorkspacePath: root}
	msgs, err := asm.BuildCompletionMessages(context.Background(), sess, []api.Message{{Role: api.MessageRoleUser, Content: "start work"}}, nil)
	testutil.FailErr(t, "BuildCompletionMessages", err)
	combined := strings.Builder{}
	for _, msg := range msgs {
		if msg.Role == api.MessageRoleSystem {
			combined.WriteString(msg.Content)
		}
	}
	if !strings.Contains(combined.String(), "Root agents policy") {
		t.Fatalf("missing root AGENTS.md chain:\n%s", combined.String())
	}
}

type integrationPolicyRoot struct{ path string }

func (integrationPolicyRoot) Applies(context.Context, string, *api.Session) bool { return true }
func (r integrationPolicyRoot) Roots(context.Context, *api.Session) ([]projectroot.RootRef, error) {
	return []projectroot.RootRef{{ID: "root", Label: "project", Path: r.path, IsPrimary: true}}, nil
}
