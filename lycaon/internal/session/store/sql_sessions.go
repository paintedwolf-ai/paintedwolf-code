package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/pkg/api"
)

// Create stores a new session.
func (s *SQL) Create(ctx context.Context, req api.CreateSessionRequest, projectID string) (*api.Session, error) {
	return s.CreateWithStatus(ctx, req, projectID, api.SessionStatusIdle)
}

// CreateWithStatus stores a session in a valid initial state.
func (s *SQL) CreateWithStatus(ctx context.Context, req api.CreateSessionRequest, projectID string, status api.SessionStatus) (*api.Session, error) {
	if !api.IsInitialSessionStatus(status) {
		return nil, &SessionStatusTransitionError{To: status}
	}
	owner, err := people.Acting(ctx, s)
	if err != nil {
		return nil, err
	}
	return s.createSession(ctx, owner.ID, req, projectID, "", "", 0, status)
}

// CreateChild stores a child session.
func (s *SQL) CreateChild(ctx context.Context, parent *api.Session, req api.SpawnChildRequest) (*api.Session, error) {
	if parent == nil {
		return nil, fmt.Errorf("parent session required")
	}
	req.AgentType = strings.TrimSpace(req.AgentType)
	if req.AgentType == "" {
		return nil, fmt.Errorf("agent_type is required")
	}
	mode := ChildSessionPosture(parent.Posture, req.AgentType)
	childReq := api.CreateSessionRequest{
		ProjectID:       parent.ProjectID,
		WorkspaceRootID: parent.WorkspaceRootID,
		Posture:         mode,
	}
	sess, err := s.createSession(ctx, parent.OwnerPersonID, childReq, parent.ProjectID, parent.ID, req.AgentType, req.MaxToolLoops, api.SessionStatusIdle)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.Prompt) != "" {
		userMsg := api.Message{
			ID:        uuid.NewString(),
			Role:      api.MessageRoleUser,
			Content:   req.Prompt,
			WorkerID:  strings.TrimSpace(req.WorkerJobID),
			CreatedAt: time.Now().UTC(),
		}
		if err := s.AppendMessages(ctx, sess.ID, userMsg); err != nil {
			return nil, err
		}
	}
	return sess, nil
}

func (s *SQL) createSession(ctx context.Context, ownerPersonID string, req api.CreateSessionRequest, projectID, parentID, agentType string, maxToolLoops int, status api.SessionStatus) (*api.Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if ownerPersonID == "" {
		return nil, ErrSessionOwnerRequired
	}
	providerID, model, err := normalizeSessionModelRef(req.ProviderID, req.Model)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	posture := req.Posture
	if strings.TrimSpace(string(posture)) == "" {
		posture = api.SessionPostureBuild
	}
	maxLoops := sql.NullInt64{}
	if maxToolLoops > 0 {
		maxLoops = sql.NullInt64{Int64: int64(maxToolLoops), Valid: true}
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		projectID = strings.TrimSpace(req.ProjectID)
	}
	if projectID == "" {
		return nil, fmt.Errorf("project_id is required")
	}
	agentType = strings.TrimSpace(agentType)
	if agentType == "" {
		agentType = defaultRootSessionAgentType(parentID)
	}
	rootID, workspacePath, err := s.resolveWorkspaceRoot(ctx, projectID, req.WorkspaceRootID)
	if err != nil {
		return nil, err
	}
	sess := &api.Session{
		ID:              uuid.NewString(),
		ProjectID:       projectID,
		OwnerPersonID:   ownerPersonID,
		WorkspaceRootID: rootID,
		WorkspacePath:   workspacePath,
		Posture:         posture,
		AgentType:       agentType,
		ProviderID:      providerID,
		Model:           model,
		Status:          status,
		ParentSessionID: parentID,
		MaxToolLoops:    maxToolLoops,
		CreatedAt:       now,
		ActivityAt:      now,
		UpdatedAt:       now,
	}
	rootNull := sql.NullString{}
	if rootID != "" {
		rootNull = sql.NullString{String: rootID, Valid: true}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	qtx := s.queries.WithTx(tx)
	if err := qtx.InsertSession(ctx, db.InsertSessionParams{
		ID:              sess.ID,
		ProjectID:       projectID,
		OwnerPersonID:   ownerPersonID,
		WorkspaceRootID: rootNull,
		Posture:         string(sess.Posture),
		WorkflowID:      sql.NullString{},
		WorkflowVersion: sql.NullString{},
		AgentType:       db.NullString(sess.AgentType),
		ProviderID:      db.NullString(sess.ProviderID),
		Model:           db.NullString(sess.Model),
		ParentSessionID: db.NullString(sess.ParentSessionID),
		MaxToolLoops:    maxLoops,
		Title:           db.NullString(sess.Title),
		Status:          string(sess.Status),
		CreatedAt:       db.FormatTime(sess.CreatedAt),
		ActivityAt:      db.FormatTime(sess.ActivityAt),
		UpdatedAt:       db.FormatTime(sess.UpdatedAt),
	}); err != nil {
		return nil, fmt.Errorf("insert session: %w", err)
	}
	if err := s.enqueueSessionEvent(ctx, tx, sess, api.SessionEventActionCreated); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit session create: %w", err)
	}
	s.outbox.Notify()
	s.reconcileProjectSpills(ctx, sess.ProjectID)
	return sess, nil
}

// Get returns a session by ID (messages loaded separately via GetMessages).
func (s *SQL) Get(ctx context.Context, id string) (*api.Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	row, err := s.queries.GetSession(ctx, id)
	if db.IsNoRows(err) {
		return nil, ErrSessionNotFound
	}
	if err != nil {
		return nil, err
	}
	sess, err := sessionFromRow(row)
	if err != nil {
		return nil, err
	}
	if err := s.hydrateWorkspacePath(ctx, sess); err != nil {
		return nil, err
	}
	untrusted, err := s.SessionUntrustedContentResult(ctx, sess.ID)
	if err != nil {
		return nil, err
	}
	sess.UntrustedContent = untrusted
	if turn, err := s.UserTurnOrdinal(ctx, sess.ID); err == nil {
		sess.CurrentTurn = turn
	}
	s.reconcileProjectSpills(ctx, sess.ProjectID)
	return sess, nil
}

// List returns all sessions.
func (s *SQL) List(ctx context.Context) ([]*api.Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rows, err := s.queries.ListSessions(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*api.Session, 0, len(rows))
	for _, r := range rows {
		sess, err := sessionFromRow(db.GetSessionRow(r))
		if err != nil {
			return nil, err
		}
		if err := s.hydrateWorkspacePath(ctx, sess); err != nil {
			return nil, err
		}
		untrusted, err := s.SessionUntrustedContentResult(ctx, sess.ID)
		if err != nil {
			return nil, err
		}
		sess.UntrustedContent = untrusted
		if turn, err := s.UserTurnOrdinal(ctx, sess.ID); err == nil {
			sess.CurrentTurn = turn
		}
		out = append(out, sess)
	}
	return out, nil
}

// ListBusySessionIDs returns one recovery page without hydrating projections.
func (s *SQL) ListBusySessionIDs(ctx context.Context, limit int) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		return []string{}, nil
	}
	return s.queries.ListBusySessionIDs(ctx, int64(limit))
}

// Delete removes a session tree atomically and enqueues its deleted events.
func (s *SQL) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	unlock := s.lockMutation(id)
	defer unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	qtx := s.queries.WithTx(tx)
	treeIDs, err := db.SessionTreeIDs(ctx, tx, id)
	if err != nil {
		return fmt.Errorf("list session tree: %w", err)
	}
	if len(treeIDs) == 0 {
		return ErrSessionNotFound
	}
	deletedSessions := make([]*api.Session, 0, len(treeIDs))
	for _, treeID := range treeIDs {
		row, getErr := qtx.GetSession(ctx, treeID)
		if getErr != nil {
			return getErr
		}
		sess, mapErr := sessionFromRow(row)
		if mapErr != nil {
			return mapErr
		}
		deletedSessions = append(deletedSessions, sess)
	}
	spillRows, err := qtx.ListSessionTreeSpillRefs(ctx, id)
	if err != nil {
		return fmt.Errorf("list session spill references: %w", err)
	}
	spillRefs := spillRefsFromTreeRows(spillRows)
	removed, err := db.DeleteSessionTree(ctx, tx, id)
	if err != nil {
		return err
	}
	if len(removed) == 0 {
		return ErrSessionNotFound
	}
	for _, sess := range deletedSessions {
		if err := s.enqueueSessionEvent(ctx, tx, sess, api.SessionEventActionDeleted); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit session delete: %w", err)
	}
	for _, treeID := range treeIDs {
		s.invalidateUntrusted(treeID)
		s.invalidateSecretExposure(treeID)
	}
	s.reclaimUnreferencedSpills(ctx, spillRefs)
	s.outbox.Notify()
	return nil
}

// sessionFromRow maps the shared session query shape.
func sessionFromRow(r db.GetSessionRow) (*api.Session, error) {
	sess := api.Session{
		ID:                   r.ID,
		Posture:              api.SessionPosture(r.Posture),
		CompactionGeneration: int(r.CompactionGeneration),
		Status:               api.SessionStatus(r.Status),
		Title:                db.StringFromNull(r.Title),
		ProjectID:            r.ProjectID,
		OwnerPersonID:        r.OwnerPersonID,
		WorkspaceRootID:      db.StringFromNull(r.WorkspaceRootID),
		AgentType:            db.StringFromNull(r.AgentType),
		ProviderID:           db.StringFromNull(r.ProviderID),
		Model:                db.StringFromNull(r.Model),
		ParentSessionID:      db.StringFromNull(r.ParentSessionID),
	}
	if r.MaxToolLoops.Valid && r.MaxToolLoops.Int64 > 0 {
		sess.MaxToolLoops = int(r.MaxToolLoops.Int64)
	}
	var err error
	sess.CreatedAt, err = db.ParseTime(r.CreatedAt)
	if err != nil {
		return nil, err
	}
	sess.ActivityAt, err = db.ParseTime(r.ActivityAt)
	if err != nil {
		return nil, err
	}
	sess.UpdatedAt, err = db.ParseTime(r.UpdatedAt)
	if err != nil {
		return nil, err
	}
	sess.ArchivedAt, err = db.TimePtrFromNull(r.ArchivedAt)
	if err != nil {
		return nil, err
	}
	if r.PinRank.Valid {
		rank := int(r.PinRank.Int64)
		sess.PinRank = &rank
	}
	sess.SeenAt, err = db.TimePtrFromNull(r.SeenAt)
	if err != nil {
		return nil, err
	}
	return &sess, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (s *SQL) enqueueSessionEvent(ctx context.Context, tx *sql.Tx, sess *api.Session, action api.SessionEventAction) error {
	return s.enqueueSessionEventWithHostError(ctx, tx, sess, action, nil)
}

func (s *SQL) enqueueSessionEventWithHostError(
	ctx context.Context,
	tx *sql.Tx,
	sess *api.Session,
	action api.SessionEventAction,
	hostError *api.SessionHostError,
) error {
	if sess == nil {
		return nil
	}
	key := events.PublishKey{Project: sess.ProjectID, Session: sess.ID}
	if s.sessionRevisions != nil {
		key.EntityRevision = s.sessionRevisions.NextSessionRevision()
	}
	qtx := s.queries.WithTx(tx)
	turn, err := qtx.GetSessionUserTurnOrdinal(ctx, sess.ID)
	if err != nil {
		return fmt.Errorf("read session event turn: %w", err)
	}
	pending := false
	if s.promptPending != nil {
		unsettled, err := qtx.ListUnsettledUserPromptSubmissionIDs(ctx, sess.ID)
		if err != nil {
			return fmt.Errorf("read session event pending prompts: %w", err)
		}
		pending = s.promptPending.PromptPendingAmong(sess.ID, unsettled)
	}
	return s.outbox.EnqueueTx(ctx, tx, api.EventTopicSession, key, api.SessionEvent{
		ID: sess.ID, ProjectID: sess.ProjectID, Action: action, Title: sess.Title, Status: sess.Status, HostError: hostError,
		CurrentTurn: int(turn), PromptPending: pending,
	})
}

var errProjectHasNoWorkspaceRoot = errors.New("project has no workspace root")

// ErrWorkspaceRootNotFound reports a requested root outside the project.
var ErrWorkspaceRootNotFound = errors.New("workspace_root_id not found for project")

func (s *SQL) resolveWorkspaceRoot(ctx context.Context, projectID, workspaceRootID string) (rootID, workspacePath string, err error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return "", "", nil
	}
	want := strings.TrimSpace(workspaceRootID)
	if want != "" {
		root, err := s.queries.GetProjectRootByID(ctx, db.GetProjectRootByIDParams{
			ID:        want,
			ProjectID: projectID,
		})
		if db.IsNoRows(err) {
			return "", "", ErrWorkspaceRootNotFound
		}
		if err != nil {
			return "", "", err
		}
		return root.ID, root.Path, nil
	}
	primary, err := s.queries.GetPrimaryProjectRoot(ctx, projectID)
	if db.IsNoRows(err) {
		return "", "", errProjectHasNoWorkspaceRoot
	}
	if err != nil {
		return "", "", err
	}
	return primary.ID, primary.Path, nil
}

func (s *SQL) hydrateWorkspacePath(ctx context.Context, sess *api.Session) error {
	if sess == nil || strings.TrimSpace(sess.ProjectID) == "" {
		return nil
	}
	if strings.TrimSpace(sess.WorkspaceRootID) != "" {
		root, err := s.queries.GetProjectRootByID(ctx, db.GetProjectRootByIDParams{
			ID:        sess.WorkspaceRootID,
			ProjectID: sess.ProjectID,
		})
		if db.IsNoRows(err) {
			return nil
		}
		if err != nil {
			return err
		}
		sess.WorkspacePath = root.Path
		return nil
	}
	_, path, err := s.resolveWorkspaceRoot(ctx, sess.ProjectID, "")
	if errors.Is(err, errProjectHasNoWorkspaceRoot) {
		sess.WorkspacePath = ""
		return nil
	}
	if err != nil {
		return err
	}
	sess.WorkspacePath = path
	return nil
}

// HasActiveProjectSessions reports whether a project has unarchived active sessions.
func (s *SQL) HasActiveProjectSessions(ctx context.Context, projectID string, activeSince time.Time) (bool, error) {
	if s == nil || s.db == nil {
		return false, nil
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return false, nil
	}
	var exists bool
	query := `
		SELECT EXISTS(
			SELECT 1 FROM sessions s
			WHERE s.project_id = ?
			  AND s.archived_at IS NULL
			  AND (
			    s.status = 'busy'
			    OR s.activity_at > ?
			    OR EXISTS (
			      SELECT 1 FROM turns t
			      WHERE t.session_id = s.id AND t.status IN ('running', 'recovering')
			    )
			  )
		)`
	err := s.db.QueryRowContext(ctx, query, projectID, db.FormatTime(activeSince)).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check active project sessions: %w", err)
	}
	return exists, nil
}
