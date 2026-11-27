package coordinatorcontrol

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/session/contextpreparation"
	sessionscope "github.com/lycaon/lycaon/internal/session/scope"
	"github.com/lycaon/lycaon/internal/toolpolicy"
	"github.com/lycaon/lycaon/pkg/api"
)

type frameProjection interface {
	ProjectCoordinatorTurnFrame(context.Context, string, *api.Session) (inject.CoordinatorTurnFrame, error)
}

// ProjectPreparationRequest assembles the disposable provider request from current host facts.
func (m *Service) ProjectPreparationRequest(ctx context.Context, id string) (*llm.ModelSelection, modelcall.CompletionRequest, bool, error) {
	var emptyReq modelcall.CompletionRequest
	if m.Model == nil || m.Model.LLMService == nil || m.Model.History == nil {
		return nil, emptyReq, false, nil
	}
	service := m.Model.LLMService
	if service.Router == nil || service.Registry == nil {
		return nil, emptyReq, false, nil
	}
	sess, err := m.Context.Sessions.Get(ctx, id)
	if err != nil || sess == nil {
		return nil, emptyReq, false, err
	}
	if sess.Status != api.SessionStatusIdle || sess.PromptPending || sess.ArchivedAt != nil || sess.IsWorkerChild() {
		return nil, emptyReq, false, nil
	}
	overlayRoots := m.Model.Workspace.SettingsRoots(ctx, sess)
	selection, err := service.Router.WithOverlayRoots(overlayRoots).ResolveSession(ctx, sess)
	if err != nil {
		return nil, emptyReq, false, err
	}
	if selection == nil || !service.Registry.CanPrepareContext(selection.ProviderID) {
		return nil, emptyReq, false, nil
	}
	projector, ok := m.Assembly.Frame.(frameProjection)
	if !ok {
		return nil, emptyReq, false, nil
	}
	frame, err := projector.ProjectCoordinatorTurnFrame(ctx, id, sess)
	if err != nil {
		return nil, emptyReq, false, err
	}
	profile, err := m.Profiles.PromptToolProfile(ctx, sess)
	if err != nil {
		return nil, emptyReq, false, err
	}
	frame.Machine = m.Profiles.ProjectMachine(ctx, sess, profile)
	if m.Assembly.ToolContext != nil {
		frame.Machine.Surface.SessionScratchDir = m.Assembly.ToolContext.ScratchDir(ctx, sess)
		frame.Machine.Surface.Fingerprint = frame.Machine.Surface.CacheFingerprint()
	}
	if err := m.populatePreparationFrame(ctx, sess, overlayRoots, &frame); err != nil {
		return nil, emptyReq, false, err
	}
	ctx = toolpolicy.WithCoordinatorTurnFrame(ctx, &frame)
	rows, err := m.Model.History.Project(ctx, sess)
	if err != nil {
		return nil, emptyReq, false, err
	}
	messages, err := m.Runtime.Assembly().BuildKnownPrefix(ctx, sess, rows, frame)
	if err != nil {
		return nil, emptyReq, false, err
	}
	req, err := m.Runtime.ProjectPreparation(ctx, sess, rows, messages, frame)
	if err != nil {
		return nil, emptyReq, false, err
	}
	req.Debug = modelcall.RequestDebug{
		SessionID: id, ProjectID: sess.ProjectID, ProjectDir: sess.WorkspacePath,
		RootSessionID: id, AgentType: sess.AgentType, ProfileID: profile,
	}
	return selection, req, true, nil
}

// BuildPreparation assembles a disposable candidate from current host facts.
func (m *Service) BuildPreparation(ctx context.Context, id string) (contextpreparation.Candidate, error) {
	empty := contextpreparation.Candidate{}
	selection, req, ok, err := m.ProjectPreparationRequest(ctx, id)
	if err != nil || !ok {
		return empty, err
	}
	payload, err := json.Marshal(struct {
		Selection any
		Request   modelcall.CompletionRequest
	}{selection, req})
	if err != nil {
		return empty, err
	}
	digest := sha256.Sum256(payload)
	service := m.Model.LLMService
	return contextpreparation.Candidate{Key: hex.EncodeToString(digest[:]), Run: func(ctx context.Context) bool {
		started := time.Now()
		result, err := service.Registry.PrepareContext(ctx, selection, req)
		slog.DebugContext(ctx, "context preparation completed", "session_id", id, "provider", selection.ProviderID, "prepared_count", result.PreparedTokens, "reused_count", result.ReusedTokens, "elapsed_ms", time.Since(started).Milliseconds(), "error", err)
		return err == nil && result.PreparedTokens > 0
	}}, nil
}

func (m *Service) populatePreparationFrame(ctx context.Context, sess *api.Session, overlayRoots []string, frame *inject.CoordinatorTurnFrame) error {
	if frame == nil || sess == nil {
		return nil
	}
	if m.Profiles != nil && sess.Mode != api.SessionModeAsk && sess.Posture != "" {
		postures, err := m.Profiles.EffectivePostures(ctx, sess)
		if err != nil {
			return err
		}
		if postures != nil {
			paths, err := postures.RulesPaths(sess.Posture)
			if err != nil {
				return err
			}
			frame.PostureRules = append([]string(nil), paths...)
		}
	}
	if workspace := m.preparationWorkspace(); workspace != nil {
		_, count, _, _ := workspace.PromptRootRows(ctx, sess)
		frame.ProjectRootCount = count
	}
	frame.OverlayRootPaths = append([]string(nil), overlayRoots...)
	return nil
}

func (m *Service) preparationWorkspace() *sessionscope.Service {
	if m != nil && m.Context != nil && m.Context.Workspace != nil {
		return m.Context.Workspace
	}
	if m != nil && m.Model != nil {
		return m.Model.Workspace
	}
	return nil
}
