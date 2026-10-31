package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/pkg/api"
)

func enqueueSessionEvent(ctx context.Context, s *sqlCore, tx *sql.Tx, sess *api.Session, action api.SessionEventAction) error {
	return enqueueSessionEventWithHostError(ctx, s, tx, sess, action, nil)
}

func enqueueSessionEventWithHostError(
	ctx context.Context,
	s *sqlCore,
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

func resolveWorkspaceRoot(ctx context.Context, s *sqlCore, projectID, workspaceRootID string) (rootID, workspacePath string, err error) {
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

func hydrateWorkspacePath(ctx context.Context, s *sqlCore, sess *api.Session) error {
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
	_, path, err := resolveWorkspaceRoot(ctx, s, sess.ProjectID, "")
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
	if s == nil || s.sqlCore == nil || s.db == nil {
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

// ReassignSessionsWorkspaceRoot retargets sessions from one root.
func (s *SQL) ReassignSessionsWorkspaceRoot(ctx context.Context, projectID, fromRootID, toRootID string) error {
	if s == nil || s.sqlCore == nil || s.db == nil {
		return nil
	}
	projectID = strings.TrimSpace(projectID)
	fromRootID = strings.TrimSpace(fromRootID)
	if projectID == "" || fromRootID == "" {
		return nil
	}
	var replacement sql.NullString
	if trimmed := strings.TrimSpace(toRootID); trimmed != "" {
		replacement = db.NullString(trimmed)
	}
	q := db.New(s.db)
	return q.ReassignSessionsWorkspaceRoot(ctx, db.ReassignSessionsWorkspaceRootParams{
		WorkspaceRootID:   replacement,
		UpdatedAt:         db.FormatTime(time.Now().UTC()),
		ProjectID:         projectID,
		WorkspaceRootID_2: db.NullString(fromRootID),
	})
}
