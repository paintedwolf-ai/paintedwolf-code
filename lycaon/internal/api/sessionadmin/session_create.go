package sessionadmin

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/projectview"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectcontrib"
	"github.com/lycaon/lycaon/internal/session"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/sourceledger"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Lifecycle) HandleCreateSession(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	perf := observability.StartPerformanceOperation("session.create.accept", nil)
	outcome := "error"
	projectID := ""
	var sessionID string
	defer func() {
		attrs := []any{"outcome", outcome, "http_path", r.URL.Path}
		if projectID != "" {
			attrs = append(attrs, "project_id", projectID)
		}
		if sessionID != "" {
			attrs = append(attrs, "session_id", sessionID)
		}
		perf.End(outcome)
		observability.LogLatency("session_bind", "create session", started, attrs...)
	}()

	var req wire.CreateSessionRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}

	projectID = strings.TrimSpace(req.ProjectID)
	req.ProviderID = strings.TrimSpace(req.ProviderID)
	req.Model = strings.TrimSpace(req.Model)
	switch {
	case projectID == "":
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "project_id is required")
		return
	case req.Posture == "":
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "posture is required")
		return
	case !validSessionPosture(req.Posture):
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "invalid session posture")
		return
	case (req.ProviderID == "") != (req.Model == ""):
		s.responses.FailReason(w, wire.ApiErrorCodeInvalidRequest, "provider_id and model must be set together")
		return
	}
	if req.ProviderID != "" {
		if err := s.LLMService.ValidateModelRef(r.Context(), llm.ModelRef{
			ProviderID: req.ProviderID,
			Model:      req.Model,
		}, llm.PolicySlotCoordinator); err != nil {
			s.responses.ModelAssignmentError(w, r, err)
			return
		}
	}
	perf.Mark("validate")

	sess, err := s.createPreparingSession(r.Context(), req)
	if err != nil {
		s.WriteSessionCreationError(w, r, err)
		return
	}
	perf.Mark("persist")
	sessionID = sess.ID
	perf.SetDimension("session_id", sessionID)
	perf.SetDimension("project_id", projectID)
	s.PublishSessionCreated(r.Context(), sess)
	outcome = "accepted"
	httpio.WriteJSON(w, http.StatusAccepted, sess)
	s.sourceWatch.ScheduleSourceInventory(r.Context(), sess.ProjectID)
	s.prepareCreatedSession(r.Context(), req, sess)
	perf.Mark("publish")
}

func (s *Lifecycle) createPreparingSession(ctx context.Context, req wire.CreateSessionRequest) (*wire.Session, error) {
	projectID := strings.TrimSpace(req.ProjectID)
	p, err := s.Projects.Get(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if len(p.Roots) > 0 {
		rootID, _, resolveErr := project.ResolveWorkspaceRoot(p, req.WorkspaceRootID)
		if resolveErr != nil {
			if errors.Is(resolveErr, project.ErrNotFound) {
				return nil, sessionstore.ErrWorkspaceRootNotFound
			}
			return nil, resolveErr
		}
		req.WorkspaceRootID = rootID
	}
	sess, err := s.Store.CreateWithStatus(ctx, req, projectID, wire.SessionStatusPreparing)
	if err != nil {
		return nil, err
	}
	if s.sourceWorkspace.SourceLedger != nil {
		if _, checkpointErr := s.sourceWorkspace.SourceLedger.CreateStructuralCheckpoint(ctx, sourceledger.StructuralCheckpointInput{
			ProjectID: projectID, Kind: sourceledger.CheckpointSession,
			Label: "Task start", SessionID: sess.ID,
		}); checkpointErr != nil {
			slog.WarnContext(ctx, "create task review checkpoint", "project_id", projectID, "session_id", sess.ID, "err", checkpointErr)
		}
	}
	s.SessionView.HydrateSessionWorkspace(ctx, sess)
	return sess, nil
}

func (s *Lifecycle) prepareCreatedSession(parent context.Context, req wire.CreateSessionRequest, sess *wire.Session) {
	s.background.Go(parent, func(ctx context.Context) {
		perf := observability.StartPerformanceOperation("session.create.prepare", map[string]string{
			"project_id": sess.ProjectID, "session_id": sess.ID,
		})
		outcome := "error"
		defer func() { perf.End(outcome) }()
		p, err := s.Projects.Get(ctx, sess.ProjectID)
		if err == nil && len(p.Roots) > 0 {
			rootID, workspacePath, resolveErr := project.ResolveWorkspaceRoot(p, req.WorkspaceRootID)
			if resolveErr != nil {
				err = resolveErr
			} else {
				req.WorkspaceRootID = rootID
				err = s.prepareSessionWorkspace(ctx, p, rootID, workspacePath)
			}
		}
		perf.Mark("workspace")
		if err == nil {
			err = s.Projects.TouchLastOpened(ctx, sess.ProjectID)
			if err == nil {
				projectview.PublishTouch(s.Projects, s.Events, ctx, sess.ProjectID)
			}
		}
		perf.Mark("project_touch")
		if err == nil {
			// A build session that never attached is not prepared.
			err = s.AttachAmbientOnSessionCreate(ctx, req, sess.ID)
		}
		perf.Mark("ambient_attach")
		status := wire.SessionStatusIdle
		if err != nil {
			status = wire.SessionStatusError
			slog.ErrorContext(ctx, "prepare session", "session_id", sess.ID, "err", err)
		}
		var hostError *wire.SessionHostError
		if err != nil {
			notice := wire.NewSessionHostError(wire.NoticeCodeSessionPreparationFailed)
			hostError = &notice
		}
		updateErr := s.Store.SetSessionStatusEvent(ctx, sess.ID, status, hostError)
		if updateErr != nil {
			slog.ErrorContext(ctx, "finish session preparation", "session_id", sess.ID, "err", updateErr)
			return
		}
		s.publishSessionPrepared(ctx, sess, status, err)
		perf.Mark("persist_publish")
		if err == nil {
			outcome = "ok"
		}
	})
}

func (s *Lifecycle) publishSessionPrepared(ctx context.Context, sess *wire.Session, status wire.SessionStatus, prepareErr error) {
	if s.Events == nil || sess == nil {
		return
	}
	if sessionMutationEventsOutboxed(s.Store) {
		return
	}
	event := wire.SessionEvent{
		ID: sess.ID, ProjectID: sess.ProjectID, Action: wire.SessionEventActionUpdated, Status: status,
	}
	if prepareErr != nil {
		hostErr := wire.NewSessionHostError(wire.NoticeCodeSessionPreparationFailed)
		event.HostError = &hostErr
	}
	key := events.PublishKey{Project: strings.TrimSpace(sess.ProjectID), Session: sess.ID}
	_ = s.Events.Publish(ctx, wire.EventTopicSession, key, event)
}

// materializeSession prepares a session synchronously for blueprint launch,
// which attaches the blueprint's own workflow rather than the ambient one.
func (s *Lifecycle) MaterializeSession(
	ctx context.Context,
	req wire.CreateSessionRequest,
) (_ *wire.Session, retErr error) {
	projectID := strings.TrimSpace(req.ProjectID)
	p, err := s.Projects.Get(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if len(p.Roots) > 0 {
		rootID, workspacePath, resolveErr := project.ResolveWorkspaceRoot(p, req.WorkspaceRootID)
		if resolveErr != nil {
			if errors.Is(resolveErr, project.ErrNotFound) {
				return nil, sessionstore.ErrWorkspaceRootNotFound
			}
			return nil, resolveErr
		}
		if req.WorkspaceRootID == "" {
			req.WorkspaceRootID = rootID
		}
		if err := s.prepareSessionWorkspace(ctx, p, rootID, workspacePath); err != nil {
			return nil, err
		}
	}
	if err := s.Projects.TouchLastOpened(ctx, projectID); err != nil {
		return nil, err
	}
	projectview.PublishTouch(s.Projects, s.Events, ctx, projectID)

	sess, err := s.Store.Create(ctx, req, projectID)
	if err != nil {
		return nil, err
	}
	defer func() {
		if retErr != nil {
			s.DiscardMaterializedSession(ctx, sess.ID)
		}
	}()
	s.SessionView.HydrateSessionWorkspace(ctx, sess)
	if path := strings.TrimSpace(sess.WorkspacePath); path != "" {
		if err := s.Store.UpdateSession(ctx, sess.ID, func(st *wire.Session) {
			st.WorkspacePath = path
			st.WorkspaceRootID = strings.TrimSpace(sess.WorkspaceRootID)
		}); err != nil {
			return nil, err
		}
	}
	return sess, nil
}

func (s *Lifecycle) DiscardMaterializedSession(ctx context.Context, sessionID string) {
	cleanupCtx := context.WithoutCancel(ctx)
	if err := s.Store.Delete(cleanupCtx, sessionID); err != nil {
		slog.WarnContext(cleanupCtx, "discard incomplete session", "session_id", sessionID, "err", err)
	}
}

func (s *Lifecycle) prepareSessionWorkspace(
	ctx context.Context,
	p *project.Project,
	rootID string,
	workspacePath string,
) error {
	s.Git.WarmRepoBrief(workspacePath)
	if s.FileAgeWarmer != nil {
		s.FileAgeWarmer(ctx, workspacePath)
	}
	overlay, err := project.ResolveProjectOverlay(p, rootID)
	if err != nil {
		return err
	}
	if err := overlay.CheckCompatibility(); err != nil {
		return err
	}
	if err := s.Sessions.Profiles.WarmPostureOverlayForProject(p, workspacePath); err != nil {
		return err
	}
	if s.ProjectRules == nil || !requestscope.ProjectSurfaceApplies(s.Settings, p, projectcontrib.SurfaceProjectSettings) {
		return nil
	}
	return s.ProjectRules.WarmOverlays(overlay.Paths)
}

func (s *Lifecycle) PublishSessionCreated(ctx context.Context, sess *wire.Session) {
	if s.Events == nil || sess == nil {
		return
	}
	if sessionMutationEventsOutboxed(s.Store) {
		return
	}
	key := events.PublishKey{Project: strings.TrimSpace(sess.ProjectID), Session: sess.ID}
	_ = s.Events.Publish(ctx, wire.EventTopicSession, key, wire.SessionEvent{
		ID:        sess.ID,
		ProjectID: sess.ProjectID,
		Action:    wire.SessionEventActionCreated,
		Title:     sess.Title,
		Status:    sess.Status,
	})
}

func sessionMutationEventsOutboxed(store session.Store) bool {
	return store != nil && store.MutationEventsOutboxed()
}

func (s *Lifecycle) WriteSessionCreationError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, project.ErrNotFound):
		s.responses.Fail(w, wire.ApiErrorCodeProjectNotFound, "project not found")
	case errors.Is(err, sessionstore.ErrWorkspaceRootNotFound):
		s.responses.Fail(w, wire.ApiErrorCodeRootNotFound, "workspace folder is not attached to this project")
	case s.responses.OverlayFormatError(w, err):
		return
	default:
		s.responses.InternalError(w, r, err)
	}
}

func validSessionPosture(mode wire.SessionPosture) bool {
	switch mode {
	case wire.SessionPostureSpec,
		wire.SessionPostureVet,
		wire.SessionPostureOrchestrate,
		wire.SessionPostureBuild:
		return true
	default:
		return false
	}
}
