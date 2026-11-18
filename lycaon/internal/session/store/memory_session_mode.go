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
	return resolveEffectiveModeLocked(s.memoryCore, id)
}

func resolveEffectiveModeLocked(s *memoryCore, id string) (RootAuthority, error) {
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
	root, err := resolveEffectiveModeLocked(core, id)
	if err != nil {
		return nil, err
	}
	if root.RootSessionID != id {
		return nil, ErrChildSessionMode
	}
	if _, err := root.WithModeIntent(&mode); err != nil {
		return nil, err
	}
	sess := core.sessions[id]
	if mode != sess.Mode {
		sess.Mode = mode
		sess.UpdatedAt = time.Now().UTC()
	}
	return projectMemorySession(core, sess), nil
}

func admitModeLocked(s *memoryCore, id string, intent *api.SessionMode) error {
	root, err := resolveEffectiveModeLocked(s, id)
	if err != nil {
		return err
	}
	if intent != nil && root.RootSessionID != id {
		return ErrChildSessionMode
	}
	mode, err := root.WithModeIntent(intent)
	if err != nil {
		return err
	}
	row := s.sessions[root.RootSessionID]
	if !row.Started || row.Mode != mode {
		row.Mode, row.Started = mode, true
		row.UpdatedAt = time.Now().UTC()
	}
	return nil
}
