//go:build integration

package governance_test

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/assembly"
	"github.com/lycaon/lycaon/internal/governance"
	"github.com/lycaon/lycaon/internal/prompts"
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
	injects := prompts.NewInjectRenderer(engine)

	deps := assembly.AssemblyDeps{
		Prompts: engine,
		Injects: injects,
		AgentsMDIndex: func(_ context.Context, _ *api.Session) (api.Message, bool) {
			index, err := governance.ListIndex(context.Background(), root)
			if err != nil {
				return api.Message{}, false
			}
			block, err := governance.BuildIndexInject(context.Background(), injects, "sess-inject-test", index)
			if err != nil || strings.TrimSpace(block.Content) == "" {
				return api.Message{}, false
			}
			return api.Message{Role: api.MessageRoleSystem, Content: block.Content}, true
		},
		AgentsMDChain: func(_ context.Context, _ *api.Session, relPath string) (api.Message, error) {
			block, err := governance.BuildChainInject(context.Background(), injects, "sess-inject-test", root, relPath, governance.DefaultAgentsMDInjectMaxBodyBytes)
			return api.Message{Role: api.MessageRoleSystem, Content: block.Content}, err
		},
	}
	asm := &assembly.AssemblyEngine{}
	asm.SetDeps(deps)
	sess := &api.Session{ID: "sess-chain", WorkspacePath: root}
	history := []api.Message{
		{Role: api.MessageRoleUser, Content: "edit backend"},
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "write", Args: map[string]any{"path": "lycaon/foo.go", "content": "package foo"}},
		}},
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
	injects := prompts.NewInjectRenderer(engine)
	asm := &assembly.AssemblyEngine{}
	asm.SetDeps(assembly.AssemblyDeps{
		Prompts: engine,
		AgentsMDChain: func(_ context.Context, _ *api.Session, relPath string) (api.Message, error) {
			block, err := governance.BuildChainInject(context.Background(), injects, "sess-inject-test", root, relPath, governance.DefaultAgentsMDInjectMaxBodyBytes)
			return api.Message{Role: api.MessageRoleSystem, Content: block.Content}, err
		},
	})
	sess := &api.Session{ID: "sess-root", WorkspacePath: root}
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
