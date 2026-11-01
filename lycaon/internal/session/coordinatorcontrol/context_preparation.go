package coordinatorcontrol

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/session/contextpreparation"
	"github.com/lycaon/lycaon/internal/toolpolicy"
	"github.com/lycaon/lycaon/pkg/api"
)

type frameProjection interface {
	ProjectCoordinatorTurnFrame(context.Context, string, *api.Session) (inject.CoordinatorTurnFrame, error)
}

// BuildPreparation assembles a disposable candidate from current host facts.
func (m *Service) BuildPreparation(ctx context.Context, id string) (contextpreparation.Candidate, error) {
	empty := contextpreparation.Candidate{}
	if m.Model == nil || m.Model.LLMService == nil || m.Model.History == nil {
		return empty, nil
	}
	service := m.Model.LLMService
	if service.Router == nil || service.Registry == nil {
		return empty, nil
	}
	sess, err := m.Context.Sessions.Get(ctx, id)
	if err != nil || sess == nil {
		return empty, err
	}
	if sess.Status != api.SessionStatusIdle || sess.PromptPending || sess.ArchivedAt != nil || sess.IsWorkerChild() {
		return empty, nil
	}
	selection, err := service.Router.WithOverlayRoots(m.Model.Workspace.SettingsRoots(ctx, sess)).ResolveSession(ctx, sess)
	if err != nil {
		return empty, err
	}
	if selection == nil || !service.Registry.CanPrepareContext(selection.ProviderID) {
		return empty, nil
	}
	projector, ok := m.Assembly.Frame.(frameProjection)
	if !ok {
		return empty, nil
	}
	frame, err := projector.ProjectCoordinatorTurnFrame(ctx, id, sess)
	if err != nil {
		return empty, err
	}
	profile, err := m.Profiles.PromptToolProfile(ctx, sess)
	if err != nil {
		return empty, err
	}
	frame.Machine = m.Profiles.ProjectMachine(ctx, sess, profile)
	if m.Assembly.ToolContext != nil {
		frame.Machine.Surface.SessionScratchDir = m.Assembly.ToolContext.ScratchDir(ctx, sess)
	}
	ctx = toolpolicy.WithCoordinatorTurnFrame(ctx, &frame)
	rows, err := m.Model.History.Project(ctx, sess)
	if err != nil {
		return empty, err
	}
	messages, err := m.Runtime.Assembly().BuildKnownPrefix(ctx, sess, rows, frame)
	if err != nil {
		return empty, err
	}
	metas, err := m.Assembly.ProjectPromptTools(ctx, sess, frame.Machine)
	if err != nil {
		return empty, err
	}
	req := modelcall.CompletionRequest{Messages: messages, Tools: metas, Debug: modelcall.RequestDebug{
		SessionID: id, ProjectID: sess.ProjectID, ProjectDir: sess.WorkspacePath,
		RootSessionID: id, AgentType: sess.AgentType, ProfileID: profile,
	}}
	payload, err := json.Marshal(struct {
		Selection any
		Request   modelcall.CompletionRequest
	}{selection, req})
	if err != nil {
		return empty, err
	}
	digest := sha256.Sum256(payload)
	return contextpreparation.Candidate{Key: hex.EncodeToString(digest[:]), Run: func(ctx context.Context) bool {
		started := time.Now()
		result, err := service.Registry.PrepareContext(ctx, selection, req)
		slog.DebugContext(ctx, "context preparation completed", "session_id", id, "provider", selection.ProviderID, "prepared_tokens", result.PreparedTokens, "reused_tokens", result.ReusedTokens, "elapsed_ms", time.Since(started).Milliseconds(), "error", err)
		return err == nil && result.PreparedTokens > 0
	}}, nil
}
