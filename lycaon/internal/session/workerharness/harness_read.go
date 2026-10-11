package workerharness

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// Read performs one ordinary read inside a scripted worker child so
// the observation lands in the child's transcript and evidence ledger exactly as
// a model-driven read would. It returns the minted evidence handle and content.
func (m *Service) Read(ctx context.Context, child *api.Session, job *api.WorkerTask, path string) (string, string, error) {
	if !configdir.IsHarnessChannel() {
		return "", "", fmt.Errorf("worker preparation requires an isolated harness")
	}
	profile, err := m.Profiles.PromptToolProfile(ctx, child)
	if err != nil {
		return "", "", err
	}
	tctx, err := m.Context.Build(ctx, child, profile, inject.Machine{})
	if err != nil {
		return "", "", err
	}
	tctx.Identity.WorkerJobID = job.ID
	tctx, err = m.Workspaces.Enrich(ctx, child, tctx)
	if err != nil {
		return "", "", err
	}
	tctx.Identity.ToolCallID = uuid.NewString()
	tctx.Identity.MessageID = uuid.NewString()
	tctx.Effects.Out = &tools.ToolInvocationOut{}
	args := map[string]any{"path": path}
	content, err := m.tools.Run(ctx, "read", args, tctx)
	if err != nil {
		return "", "", err
	}
	projectDir, err := m.Workspace.ActivePath(ctx, child)
	if err != nil {
		return "", "", err
	}
	handle, patched, err := m.store.CommitEvidenceToolResult(ctx, child.ID, projectDir, "read", args, content)
	if err != nil {
		return "", "", err
	}
	assistant := api.Message{
		ID: tctx.Identity.MessageID, Role: api.MessageRoleAssistant, Origin: api.MessageOriginHost,
		Authority: api.ContentAuthorityNone, TrustTier: api.ContentTrustTierTrusted,
		ToolCalls: []api.ToolCall{{Name: "read", ID: tctx.Identity.ToolCallID, Args: args}},
	}
	result := api.Message{
		ID: uuid.NewString(), Role: api.MessageRoleTool, Origin: api.MessageOriginTool,
		Authority: api.ContentAuthorityNone, TrustTier: api.ContentTrustTierUntrusted, Content: patched,
		EvidenceHandles: []string{handle},
		ToolResult:      &api.ToolResult{Content: patched, Tool: "read", ToolCallID: tctx.Identity.ToolCallID, AssistantMessageID: assistant.ID, ToolArgs: args, Outcome: api.ToolResultOutcomeCompleted},
	}
	if err := m.store.AppendMessages(ctx, child.ID, assistant, result); err != nil {
		return "", "", err
	}
	return handle, patched, nil
}
