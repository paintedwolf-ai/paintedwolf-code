package sessionadmin

import (
	"context"
	"database/sql"
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
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
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

	sess, err := s.createPreparingSession(r.Context(), req, nil)
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
	s.scheduleCreatedSession(r.Context(), req, sess)
	perf.Mark("publish")
}

// SessionReservation names an ID reserved before creation and the write that
// commits with the chat.
type SessionReservation struct {
	ID     string
	Commit func(context.Context, *sql.Tx) error
}

// CreateReservedSession commits a preparing chat under a reserved ID and then
// prepares it in the background as an ordinary create does.
func (s *Lifecycle) CreateReservedSession(ctx context.Context, req wire.CreateSessionRequest, reservation SessionReservation) (*wire.Session, error) {
	sess, err := s.createPreparingSession(ctx, req, &reservation)
	if err != nil {
		return nil, err
	}
	s.PublishSessionCreated(ctx, sess)
	s.scheduleCreatedSession(ctx, req, sess)
	return sess, nil
}

// ResumePreparingSessions restarts preparation that an engine stop interrupted.
func (s *Lifecycle) ResumePreparingSessions(ctx context.Context) error {
	ids, err := s.SessionStore.ListPreparingSessionIDs(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		sess, err := s.SessionStore.Get(ctx, id)
		if err != nil {
			return err
		}
		req := wire.CreateSessionRequest{ProjectID: sess.ProjectID, WorkspaceRootID: sess.WorkspaceRootID, Posture: sess.Posture}
		s.recordTaskStart(ctx, sess)
		s.SessionView.HydrateSessionWorkspace(ctx, sess)
		s.scheduleCreatedSession(ctx, req, sess)
	}
	return nil
}

func (s *Lifecycle) scheduleCreatedSession(ctx context.Context, req wire.CreateSessionRequest, sess *wire.Session) {
	s.sourceWatch.ScheduleSourceInventory(ctx, sess.ProjectID)
	s.prepareCreatedSession(ctx, req, sess)
}

func (s *Lifecycle) createPreparingSession(ctx context.Context, req wire.CreateSessionRequest, reservation *SessionReservation) (*wire.Session, error) {
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
	var sess *wire.Session
	if reservation == nil {
		sess, err = s.SessionStore.CreateWithStatus(ctx, req, projectID, wire.SessionStatusPreparing)
	} else {
		sess, err = s.SessionStore.CreateReserved(ctx, reservation.ID, req, projectID, wire.SessionStatusPreparing, reservation.Commit)
	}
	if err != nil {
		return nil, err
	}
	s.recordTaskStart(ctx, sess)
	s.SessionView.HydrateSessionWorkspace(ctx, sess)
	return sess, nil
}

// recordTaskStart records the boundary task review compares against before any
// turn can change the workspace.
func (s *Lifecycle) recordTaskStart(ctx context.Context, sess *wire.Session) {
	if s.sourceWorkspace.SourceLedger == nil {
		return
	}
	if err := s.sourceWorkspace.SourceLedger.Checkpoints.EnsureSessionCheckpoint(ctx, sess.ProjectID, sess.ID, "Task start"); err != nil {
		slog.WarnContext(ctx, "create task review checkpoint", "project_id", sess.ProjectID, "session_id", sess.ID, "err", err)
	}
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
			var moved bool
			moved, err = s.Projects.TouchLastOpened(ctx, sess.ProjectID)
			if err == nil && moved {
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
		updateErr := s.SessionStore.SetSessionStatusEvent(ctx, sess.ID, status, hostError)
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
	if s.SessionStore.SessionEventsOutboxed() {
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
	moved, err := s.Projects.TouchLastOpened(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if moved {
		projectview.PublishTouch(s.Projects, s.Events, ctx, projectID)
	}

	sess, err := s.SessionStore.Create(ctx, req, projectID)
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
		if err := s.SessionStore.UpdateSession(ctx, sess.ID, func(st *wire.Session) {
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
	if err := s.SessionStore.Delete(cleanupCtx, sessionID); err != nil {
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
	if s.SessionStore.SessionEventsOutboxed() {
		return
	}
	key := events.PublishKey{Project: strings.TrimSpace(sess.ProjectID), Session: sess.ID}
	_ = s.Events.Publish(ctx, wire.EventTopicSession, key, wire.SessionEvent{
		ID:               sess.ID,
		ProjectID:        sess.ProjectID,
		Action:           wire.SessionEventActionCreated,
		Title:            sess.Title,
		Status:           sess.Status,
		Mode:             sess.Mode,
		Started:          &sess.Started,
		UntrustedContent: sess.UntrustedContent,
		CurrentTurn:      sess.CurrentTurn,
		UI:               sess.UI,
		PromptPending:    sess.PromptPending,
	})
}

func (s *Lifecycle) WriteSessionCreationError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case requestscope.WriteSessionModeError(s.responses, w, err):
		return
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
