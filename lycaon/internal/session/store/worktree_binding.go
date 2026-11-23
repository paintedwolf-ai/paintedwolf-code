package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/gitrepo"
	"github.com/lycaon/lycaon/internal/timelayout"
)

// WorktreeBinding is one session's execution checkout binding. Absence of a row is
// the unbound state.
type WorktreeBinding struct {
	WorktreeID   string
	SessionID    string
	ProjectID    string
	RepoID       string
	Toplevel     string
	WorktreePath string
	Branch       string
	BaseBranch   string
	State        string
	Kind         string
	Available    bool
	Managed      bool
	CreatedAt    time.Time
}

// GetWorktreeBinding returns the session's worktree binding; ok=false when unbound.
func (s *sqlSessions) GetWorktreeBinding(ctx context.Context, sessionID string) (*WorktreeBinding, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, false, nil
	}
	row, err := s.core.queries.GetSessionWorktree(ctx, sessionID)
	if err != nil {
		if db.IsNoRows(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	createdAt, err := db.ParseTime(row.CreatedAt)
	if err != nil {
		return nil, false, fmt.Errorf("parse worktree created_at: %w", err)
	}
	return &WorktreeBinding{
		WorktreeID:   row.WorktreeID,
		SessionID:    row.SessionID,
		ProjectID:    row.ProjectID,
		RepoID:       row.RepoID,
		Toplevel:     row.Toplevel,
		WorktreePath: row.WorktreePath,
		Branch:       row.Branch,
		BaseBranch:   row.BaseBranch,
		State:        row.State,
		Kind:         row.Kind,
		Available:    row.Available != 0,
		Managed:      row.Managed != 0,
		CreatedAt:    createdAt,
	}, true, nil
}

// PutWorktreeBinding inserts or updates the session's execution binding with registration evidence.
func (s *sqlSessions) PutWorktreeBinding(ctx context.Context, b WorktreeBinding) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.SessionID = strings.TrimSpace(b.SessionID)
	b.ProjectID = strings.TrimSpace(b.ProjectID)
	if b.SessionID == "" || b.ProjectID == "" {
		return fmt.Errorf("session_id and project_id required")
	}
	now := time.Now().UTC()
	createdAt := b.CreatedAt
	if createdAt.IsZero() {
		createdAt = now
	}
	repoID := strings.TrimSpace(b.RepoID)
	toplevel := strings.TrimSpace(b.Toplevel)
	worktreePath := strings.TrimSpace(b.WorktreePath)
	branch := strings.TrimSpace(b.Branch)
	baseBranch := strings.TrimSpace(b.BaseBranch)

	var reg gitrepo.RegisteredCheckout
	var hasLiveEvidence bool
	if commonDir, _, _, ok := gitrepo.CommonRepository(toplevel); ok && commonDir != "" && worktreePath != "" {
		if verified, verifiedOK := gitrepo.VerifyRegisteredCheckout(commonDir, worktreePath); verifiedOK {
			reg = verified
			hasLiveEvidence = true
		}
	}

	tx, err := s.core.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.core.queries.WithTx(tx)

	sessionTime := timelayout.Format(createdAt)
	nowText := timelayout.Format(now)
	if existingSession, sesErr := q.GetSessionWorktree(ctx, b.SessionID); sesErr == nil && b.CreatedAt.IsZero() {
		sessionTime = existingSession.CreatedAt
	}

	var targetID string
	existing, pathErr := q.GetActiveSourceWorktreeByPath(ctx, db.GetActiveSourceWorktreeByPathParams{
		ProjectID:    b.ProjectID,
		RepoID:       repoID,
		WorktreePath: worktreePath,
	})
	switch {
	case pathErr != nil && !db.IsNoRows(pathErr):
		return pathErr
	case pathErr == nil:
		if hasLiveEvidence && existing.AdminIdentity != "" && !sameRecordedIncarnation(existing.AdminIdentity, reg.AdminIdentity) {
			targetID = uuid.NewString()
			if err := q.RetireSourceWorktree(ctx, db.RetireSourceWorktreeParams{
				RetiredAt:      nowText,
				UpdatedAt:      nowText,
				SupersededByID: targetID,
				ProjectID:      b.ProjectID,
				ID:             existing.ID,
			}); err != nil {
				return err
			}
			if err := insertManagedSourceWorktree(ctx, q, targetID, b.ProjectID, repoID, toplevel, worktreePath, branch, baseBranch, sessionTime, nowText, reg); err != nil {
				return err
			}
		} else {
			targetID = existing.ID
			commonDir, adminDir := existing.CommonDir, existing.AdminDir
			commonID, adminID, wtID := existing.CommonIdentity, existing.AdminIdentity, existing.WorktreeIdentity
			if hasLiveEvidence {
				commonDir, adminDir = reg.CommonDir, reg.AdminDir
				commonID, adminID, wtID = reg.CommonIdentity, reg.AdminIdentity, reg.WorktreeIdentity
			}
			if err := q.UpdateSourceWorktreeBindingMeta(ctx, db.UpdateSourceWorktreeBindingMetaParams{
				RepoID:           repoID,
				Toplevel:         toplevel,
				WorktreePath:     worktreePath,
				Branch:           branch,
				BaseBranch:       baseBranch,
				CommonDir:        commonDir,
				AdminDir:         adminDir,
				CommonIdentity:   commonID,
				AdminIdentity:    adminID,
				WorktreeIdentity: wtID,
				UpdatedAt:        nowText,
				ProjectID:        b.ProjectID,
				ID:               targetID,
			}); err != nil {
				return err
			}
		}
	default:
		targetID = uuid.NewString()
		if err := insertManagedSourceWorktree(ctx, q, targetID, b.ProjectID, repoID, toplevel, worktreePath, branch, baseBranch, sessionTime, nowText, reg); err != nil {
			return err
		}
	}

	if err := q.UpsertSessionWorktree(ctx, db.UpsertSessionWorktreeParams{
		SessionID:  b.SessionID,
		ProjectID:  b.ProjectID,
		WorktreeID: targetID,
		CreatedAt:  sessionTime,
	}); err != nil {
		return err
	}
	return tx.Commit()
}

func insertManagedSourceWorktree(
	ctx context.Context,
	q *db.Queries,
	id, projectID, repoID, toplevel, worktreePath, branch, baseBranch, createdAt, updatedAt string,
	reg gitrepo.RegisteredCheckout,
) error {
	kind := string(gitrepo.CheckoutLinked)
	if reg.Kind != "" {
		kind = string(reg.Kind)
	}
	return q.InsertSourceWorktree(ctx, db.InsertSourceWorktreeParams{
		ID:               id,
		ProjectID:        projectID,
		RepoID:           repoID,
		Toplevel:         toplevel,
		WorktreePath:     worktreePath,
		Branch:           branch,
		BaseBranch:       baseBranch,
		CreatedAt:        createdAt,
		State:            "active",
		Kind:             kind,
		Available:        1,
		Managed:          1,
		CommonDir:        reg.CommonDir,
		AdminDir:         reg.AdminDir,
		CommonIdentity:   reg.CommonIdentity,
		AdminIdentity:    reg.AdminIdentity,
		WorktreeIdentity: reg.WorktreeIdentity,
		UpdatedAt:        updatedAt,
	})
}

func sameRecordedIncarnation(recorded, current string) bool {
	if fspath.HasCreationDiscriminator(recorded) || fspath.HasCreationDiscriminator(current) {
		return fspath.SameIncarnation(recorded, current)
	}
	return recorded == current
}

// DeleteWorktreeBinding removes a session binding while leaving the checkout registration intact.
func (s *sqlSessions) DeleteWorktreeBinding(ctx context.Context, sessionID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	return s.core.queries.DeleteSessionWorktree(ctx, sessionID)
}

// RetireWorktreeBinding removes a session binding and retires its checkout incarnation after removal.
func (s *sqlSessions) RetireWorktreeBinding(ctx context.Context, sessionID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	tx, err := s.core.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.core.queries.WithTx(tx)
	row, getErr := q.GetSessionWorktree(ctx, sessionID)
	if getErr != nil && !db.IsNoRows(getErr) {
		return getErr
	}
	if err := q.DeleteSessionWorktree(ctx, sessionID); err != nil {
		return err
	}
	if getErr == nil {
		nowText := timelayout.Format(time.Now().UTC())
		if err := q.RetireSourceWorktree(ctx, db.RetireSourceWorktreeParams{
			RetiredAt:      nowText,
			UpdatedAt:      nowText,
			SupersededByID: "",
			ProjectID:      row.ProjectID,
			ID:             row.WorktreeID,
		}); err != nil {
			return err
		}
	}
	return tx.Commit()
}
