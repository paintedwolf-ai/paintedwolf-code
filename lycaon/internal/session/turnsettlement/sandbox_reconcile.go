package turnsettlement

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
)

type ProjectSandboxReconcile func(projectID, workspacePath string)

func (m *Service) SetSandboxReconcile(fn ProjectSandboxReconcile) {
	if m == nil {
		return
	}
	m.projectSandboxReconcile = fn
}

func (m *Service) ReconcileSandbox(ctx context.Context, sessionID string) {
	if m == nil || m.projectSandboxReconcile == nil || m.store == nil {
		return
	}
	sess, err := m.store.Get(ctx, sessionID)
	if err != nil || sess == nil {
		return
	}
	projectDir, err := m.workspace.ActivePath(ctx, sess)
	if err != nil {
		return
	}
	projectDir = strings.TrimSpace(projectDir)
	if projectDir == "" {
		return
	}
	if m.workerQueue != nil {
		idle, err := workeroutcomes.ParentSessionWorkerCycleIdle(ctx, m.workerQueue, sess.ProjectID, sessionID, "")
		if err != nil || !idle {
			return
		}
	}
	m.projectSandboxReconcile(sess.ProjectID, projectDir)
}
