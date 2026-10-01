package session

import (
	"context"
	"strings"
)

type ProjectSandboxReconcile func(projectID, workspacePath string)

func (m *Manager) SetProjectSandboxReconcile(fn ProjectSandboxReconcile) {
	if m == nil {
		return
	}
	m.projectSandboxReconcile = fn
}

func (m *Manager) maybeReconcileSandboxesOnIdle(ctx context.Context, sessionID string) {
	if m == nil || m.projectSandboxReconcile == nil || m.store == nil {
		return
	}
	sess, err := m.store.Get(ctx, sessionID)
	if err != nil || sess == nil {
		return
	}
	projectDir, err := m.sessionActiveRootPath(ctx, sess)
	if err != nil {
		return
	}
	projectDir = strings.TrimSpace(projectDir)
	if projectDir == "" {
		return
	}
	if m.workerQueue != nil {
		idle, err := ParentSessionWorkerCycleIdle(ctx, m.workerQueue, sess.ProjectID, sessionID, "")
		if err != nil || !idle {
			return
		}
	}
	m.projectSandboxReconcile(sess.ProjectID, projectDir)
}
