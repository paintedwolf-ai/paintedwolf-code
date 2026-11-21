package store

import (
	"context"
	"time"

	"github.com/lycaon/lycaon/pkg/api"
)

// ResolveEffectiveMode reads verified root authority for effect admission.
func (s *Memory) ResolveEffectiveMode(ctx context.Context, id string) (RootAuthority, error) {
	if s == nil || s.memoryCore == nil {
		return RootAuthority{}, ErrSessionModeUnavailable
	}
	if err := ctx.Err(); err != nil {
		return RootAuthority{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return (memoryModeRecords{s.memoryCore}).ReadAuthority(ctx, id)
}

type memoryModeRecords struct {
	core *memoryCore
}

func (r memoryModeRecords) ReadAuthority(_ context.Context, id string) (RootAuthority, error) {
	s := r.core
	row := s.sessions[id]
	if row == nil {
		return RootAuthority{}, sessionModeUnavailable(id, ErrSessionNotFound)
	}
	root := s.sessions[row.RootSessionID]
	if root == nil || root.ParentSessionID != "" || root.RootSessionID != root.ID ||
		root.ProjectID != row.ProjectID || root.OwnerPersonID != row.OwnerPersonID ||
		!validSessionMode(root.Mode) {
		return RootAuthority{}, ErrSessionModeUnavailable
	}
	if row.ID != root.ID {
		parent := s.sessions[row.ParentSessionID]
		if parent == nil || parent.RootSessionID != root.ID || parent.ProjectID != row.ProjectID ||
			parent.OwnerPersonID != row.OwnerPersonID || row.Mode != "" || row.Started {
			return RootAuthority{}, ErrSessionModeUnavailable
		}
	}
	return RootAuthority{RootSessionID: root.ID, Mode: root.Mode, Started: root.Started}, nil
}

func (s *memorySessions) SetMode(ctx context.Context, id string, mode api.SessionMode) (*api.Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	core := s.core
	core.mu.Lock()
	defer core.mu.Unlock()
	if core.sessions[id] == nil {
		return nil, ErrSessionNotFound
	}
	return setModeSnapshot(ctx, memoryModeRecords{core}, id, mode)
}

func (r memoryModeRecords) StoreAuthority(_ context.Context, root RootAuthority) error {
	row := r.core.sessions[root.RootSessionID]
	row.Mode, row.Started = root.Mode, root.Started
	row.UpdatedAt = time.Now().UTC()
	return nil
}

func (r memoryModeRecords) SessionSnapshot(ctx context.Context, id string) (*api.Session, error) {
	sess := r.core.sessions[id]
	if sess == nil {
		return nil, ErrSessionNotFound
	}
	view := *sess
	view.UntrustedContent = r.core.untrustedRecords[id] > 0
	view.CurrentTurn = currentUserTurnLocked(r.core, id)
	view.Mode, view.Started = "", false
	if root, err := r.ReadAuthority(ctx, id); err == nil {
		view.Mode, view.Started = root.Mode, root.Started
	}
	return &view, nil
}
