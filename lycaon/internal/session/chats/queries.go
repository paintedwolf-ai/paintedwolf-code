package chats

import (
	"context"
	"strings"
	"time"

	"github.com/lycaon/lycaon/pkg/api"
)

// CreateForProject opens a project-bound session.
func (m *Service) CreateForProject(ctx context.Context, projectID string, mode api.SessionPosture) (*api.Session, error) {
	projectID = strings.TrimSpace(projectID)
	sess, err := m.store.Create(ctx, api.CreateSessionRequest{
		ProjectID: projectID,
		Posture:   mode,
	}, projectID)
	if err != nil {
		return nil, err
	}
	m.policy.Warm(ctx, sess)
	return sess, nil
}

// Get returns a session by ID.
func (m *Service) Get(ctx context.Context, id string) (*api.Session, error) {
	return m.store.Get(ctx, id)
}

// HasActiveProjectSessions reports whether a project has unarchived active sessions.
func (m *Service) HasActiveProjectSessions(ctx context.Context, projectID string, activeSince time.Time) (bool, error) {
	if m == nil || m.store == nil {
		return false, nil
	}
	return m.store.HasActiveProjectSessions(ctx, projectID, activeSince)
}
