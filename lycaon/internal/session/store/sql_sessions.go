package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/pkg/api"
)

// Create stores a new session.
func (s *sqlSessions) Create(ctx context.Context, req api.CreateSessionRequest, projectID string) (*api.Session, error) {
	return s.CreateWithStatus(ctx, req, projectID, api.SessionStatusIdle)
}

// CreateWithStatus stores a session in a valid initial state.
func (s *sqlSessions) CreateWithStatus(ctx context.Context, req api.CreateSessionRequest, projectID string, status api.SessionStatus) (*api.Session, error) {
	if !api.IsInitialSessionStatus(status) {
		return nil, &SessionStatusTransitionError{To: status}
	}
	owner, err := people.Acting(ctx, s.core)
	if err != nil {
		return nil, err
	}
	return createSession(ctx, s.core, owner.ID, req, projectID, "", "", 0, status)
}

// CreateChild stores a child session.
func (s *sqlSessions) CreateChild(ctx context.Context, parent *api.Session, req api.SpawnChildRequest) (*api.Session, error) {
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
	sess, err := createSession(ctx, s.core, parent.OwnerPersonID, childReq, parent.ProjectID, parent.ID, req.AgentType, req.MaxToolLoops, api.SessionStatusIdle)
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
		if err := (&sqlTranscript{core: s.core}).AppendMessages(ctx, sess.ID, userMsg); err != nil {
			return nil, err
		}
	}
	return sess, nil
}

func createSession(ctx context.Context, s *sqlCore, ownerPersonID string, req api.CreateSessionRequest, projectID, parentID, agentType string, maxToolLoops int, status api.SessionStatus) (*api.Session, error) {
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
	rootID, workspacePath, err := resolveWorkspaceRoot(ctx, s, projectID, req.WorkspaceRootID)
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
	if err := enqueueSessionEvent(ctx, s, tx, sess, api.SessionEventActionCreated); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit session create: %w", err)
	}
	s.outbox.Notify()
	reconcileProjectSpills(ctx, s, sess.ProjectID)
	return sess, nil
}

// Get returns a session by ID (messages loaded separately via GetMessages).
func (s *sqlSessions) Get(ctx context.Context, id string) (*api.Session, error) {
	return getSessionSQL(ctx, s.core, id)
}

func getSessionSQL(ctx context.Context, s *sqlCore, id string) (*api.Session, error) {
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
	if err := hydrateWorkspacePath(ctx, s, sess); err != nil {
		return nil, err
	}
	untrusted, err := sessionUntrustedContentResultSQL(ctx, s, sess.ID)
	if err != nil {
		return nil, err
	}
	sess.UntrustedContent = untrusted
	if turnSource, err := (&sqlTranscript{core: s}).ReadSourceTurn(ctx, sess.ID, 0); err == nil {
		sess.CurrentTurn = turnSource.Number
	}
	reconcileProjectSpills(ctx, s, sess.ProjectID)
	return sess, nil
}

// List returns all sessions.
func (s *sqlSessions) List(ctx context.Context) ([]*api.Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rows, err := s.core.queries.ListSessions(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*api.Session, 0, len(rows))
	for _, r := range rows {
		sess, err := sessionFromRow(db.GetSessionRow(r))
		if err != nil {
			return nil, err
		}
		if err := hydrateWorkspacePath(ctx, s.core, sess); err != nil {
			return nil, err
		}
		untrusted, err := sessionUntrustedContentResultSQL(ctx, s.core, sess.ID)
		if err != nil {
			return nil, err
		}
		sess.UntrustedContent = untrusted
		if turnSource, err := (&sqlTranscript{core: s.core}).ReadSourceTurn(ctx, sess.ID, 0); err == nil {
			sess.CurrentTurn = turnSource.Number
		}
		out = append(out, sess)
	}
	return out, nil
}

// ListBusySessionIDs returns one recovery page without hydrating projections.
func (s *sqlSessions) ListBusySessionIDs(ctx context.Context, limit int) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		return []string{}, nil
	}
	return s.core.queries.ListBusySessionIDs(ctx, int64(limit))
}

// Delete removes a session tree atomically and enqueues its deleted events.
func (s *sqlSessions) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	core := s.core
	unlock := core.lockMutation(id)
	defer unlock()
	tx, err := core.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	qtx := core.queries.WithTx(tx)
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
		if err := enqueueSessionEvent(ctx, core, tx, sess, api.SessionEventActionDeleted); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit session delete: %w", err)
	}
	for _, treeID := range treeIDs {
		invalidateUntrusted(core, treeID)
		invalidateSecretExposure(core, treeID)
	}
	reclaimUnreferencedSpills(ctx, core, spillRefs)
	core.outbox.Notify()
	return nil
}
