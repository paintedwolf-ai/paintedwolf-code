package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/db"
)

// WorktreeBinding is one session's worktree, as persisted. Absence of a row is
// the unbound state; there is no "bound: false" row.
type WorktreeBinding struct {
	WorktreeID   string
	SessionID    string
	ProjectID    string
	RepoID       string
	Toplevel     string
	WorktreePath string
	Branch       string
	BaseBranch   string
	CreatedAt    time.Time
}

// GetWorktreeBinding returns the session's worktree binding; ok=false when unbound.
func (s *SQL) GetWorktreeBinding(ctx context.Context, sessionID string) (*WorktreeBinding, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, false, nil
	}
	row, err := s.queries.GetSessionWorktree(ctx, sessionID)
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
		CreatedAt:    createdAt,
	}, true, nil
}

// PutWorktreeBinding inserts or replaces the session's binding.
func (s *SQL) PutWorktreeBinding(ctx context.Context, b WorktreeBinding) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.SessionID = strings.TrimSpace(b.SessionID)
	b.ProjectID = strings.TrimSpace(b.ProjectID)
	if b.SessionID == "" || b.ProjectID == "" {
		return fmt.Errorf("session_id and project_id required")
	}
	createdAt := b.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	id, err := q.UpsertSourceWorktree(ctx, db.UpsertSourceWorktreeParams{
		ID: uuid.NewString(), ProjectID: b.ProjectID, RepoID: strings.TrimSpace(b.RepoID),
		Toplevel: strings.TrimSpace(b.Toplevel), WorktreePath: strings.TrimSpace(b.WorktreePath),
		Branch: strings.TrimSpace(b.Branch), BaseBranch: strings.TrimSpace(b.BaseBranch), CreatedAt: db.FormatTime(createdAt),
	})
	if err != nil {
		return err
	}
	if err := q.UpsertSessionWorktree(ctx, db.UpsertSessionWorktreeParams{
		SessionID: b.SessionID, ProjectID: b.ProjectID, WorktreeID: id, CreatedAt: db.FormatTime(createdAt),
	}); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteWorktreeBinding removes a session binding.
func (s *SQL) DeleteWorktreeBinding(ctx context.Context, sessionID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	return s.queries.DeleteSessionWorktree(ctx, sessionID)
}

// GetWorktreeBinding returns the session's worktree binding; ok=false when unbound.
func (s *Memory) GetWorktreeBinding(ctx context.Context, sessionID string) (*WorktreeBinding, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, false, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, ok := s.worktreeBindings[sessionID]
	if !ok {
		return nil, false, nil
	}
	cp := b
	return &cp, true, nil
}

// PutWorktreeBinding inserts or replaces the session's binding.
func (s *Memory) PutWorktreeBinding(ctx context.Context, b WorktreeBinding) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.SessionID = strings.TrimSpace(b.SessionID)
	b.ProjectID = strings.TrimSpace(b.ProjectID)
	if b.SessionID == "" || b.ProjectID == "" {
		return fmt.Errorf("session_id and project_id required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sessions[b.SessionID]; !ok {
		return ErrSessionNotFound
	}
	if existing, ok := s.worktreeBindings[b.SessionID]; ok && b.CreatedAt.IsZero() {
		b.CreatedAt = existing.CreatedAt
	}
	if b.CreatedAt.IsZero() {
		b.CreatedAt = time.Now().UTC()
	}
	b.RepoID = strings.TrimSpace(b.RepoID)
	b.Toplevel = strings.TrimSpace(b.Toplevel)
	b.WorktreePath = strings.TrimSpace(b.WorktreePath)
	b.Branch = strings.TrimSpace(b.Branch)
	b.BaseBranch = strings.TrimSpace(b.BaseBranch)
	key := b.ProjectID + "\x00" + b.WorktreePath
	id := s.worktreeIdentities[key]
	if id == "" {
		id = uuid.NewString()
		s.worktreeIdentities[key] = id
	}
	b.WorktreeID = id
	s.worktreeBindings[b.SessionID] = b
	return nil
}

// DeleteWorktreeBinding removes a session binding.
func (s *Memory) DeleteWorktreeBinding(ctx context.Context, sessionID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.worktreeBindings, sessionID)
	return nil
}
