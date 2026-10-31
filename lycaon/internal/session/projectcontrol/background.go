package projectcontrol

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectroot"
	sessiontree "github.com/lycaon/lycaon/internal/session/tree"
	"github.com/lycaon/lycaon/pkg/api"
)

type hydratedSessionMeta struct {
	id       string
	title    string
	archived bool
}

func (m *Service) resolveOwningSessionMeta(
	ctx context.Context,
	sessionID, rootSessionID string,
	cache map[string]hydratedSessionMeta,
) hydratedSessionMeta {
	ownerID := strings.TrimSpace(rootSessionID)
	if ownerID == "" {
		ownerID = strings.TrimSpace(sessionID)
	}
	if m != nil && m.store != nil && sessionID != "" {
		if resolved := strings.TrimSpace(sessiontree.RootID(ctx, m.store, sessionID)); resolved != "" {
			ownerID = resolved
		}
	}
	if ownerID == "" {
		return hydratedSessionMeta{}
	}
	if cached, ok := cache[ownerID]; ok {
		return cached
	}
	meta := hydratedSessionMeta{id: ownerID}
	if m != nil && m.store != nil {
		if sess, err := m.store.Get(ctx, ownerID); err == nil && sess != nil {
			meta.title = strings.TrimSpace(sess.Title)
			meta.archived = sess.ArchivedAt != nil
		} else if sessionID != "" && sessionID != ownerID {
			if sess, err := m.store.Get(ctx, sessionID); err == nil && sess != nil {
				meta.title = strings.TrimSpace(sess.Title)
				meta.archived = sess.ArchivedAt != nil
			}
		}
	}
	if cache != nil {
		cache[ownerID] = meta
	}
	return meta
}

func (m *Service) hydrateProjectProcess(
	ctx context.Context,
	row bgprocess.ProjectProcess,
	roots []projectroot.RootRef,
	sessionCache map[string]hydratedSessionMeta,
) api.ProjectBackgroundProcess {
	meta := m.resolveOwningSessionMeta(ctx, row.SessionID, row.RootSessionID, sessionCache)
	var rootID, workingDir string
	if matched, rel, ok := projectroot.MatchRoot(roots, row.Cwd); ok {
		rootID = matched.ID
		workingDir = rel
	} else if strings.TrimSpace(row.Cwd) != "" {
		workingDir = row.Cwd
	}
	item := api.ProjectBackgroundProcess{
		ProcessID:       row.Handle,
		ProjectID:       row.ProjectID,
		SessionID:       meta.id,
		SessionTitle:    meta.title,
		SessionArchived: meta.archived,
		Command:         row.Command,
		RootID:          rootID,
		WorkingDir:      workingDir,
		Ports:           append([]string(nil), row.Ports...),
		StartedAt:       row.StartedAt,
		EndedAt:         row.EndedAt,
		Running:         row.Running,
	}
	if row.HasExit {
		code := row.ExitCode
		item.ExitCode = &code
		item.TerminationReason = api.BackgroundProcessTerminationReason(row.TerminationReason)
	}
	return item
}

// ListProjectBackground returns the running and recently ended background commands for projectID.
func (m *Service) ListProjectBackground(
	ctx context.Context,
	projectID string,
) (api.ProjectBackgroundProcessListResponse, error) {
	if m == nil || m.projects == nil {
		return api.ProjectBackgroundProcessListResponse{}, fmt.Errorf("project registry not configured")
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return api.ProjectBackgroundProcessListResponse{}, project.ErrNotFound
	}
	p, err := m.projects.Get(ctx, projectID)
	if err != nil {
		return api.ProjectBackgroundProcessListResponse{}, err
	}
	roots := project.RootRefsFrom(p)
	if m.processes == nil {
		return api.ProjectBackgroundProcessListResponse{
			Processes: []api.ProjectBackgroundProcess{},
			Revision:  0,
		}, nil
	}
	rows, rev := m.processes.ListProjectBackground(ctx, projectID)
	sessionCache := make(map[string]hydratedSessionMeta, len(rows))
	items := make([]api.ProjectBackgroundProcess, 0, len(rows))
	for _, row := range rows {
		items = append(items, m.hydrateProjectProcess(ctx, row, roots, sessionCache))
	}
	return api.ProjectBackgroundProcessListResponse{
		Processes: items,
		Revision:  rev,
	}, nil
}

// RunningProjectBackground returns only the currently running background commands for projectID.
func (m *Service) RunningProjectBackground(
	ctx context.Context,
	projectID string,
) ([]api.ProjectBackgroundProcess, error) {
	resp, err := m.ListProjectBackground(ctx, projectID)
	if err != nil {
		return nil, err
	}
	var running []api.ProjectBackgroundProcess
	for _, proc := range resp.Processes {
		if proc.Running {
			running = append(running, proc)
		}
	}
	return running, nil
}

// StopProjectBackgroundProcess stops a background command belonging to projectID, recording stopped_by_user.
func (m *Service) StopProjectBackgroundProcess(
	ctx context.Context,
	projectID, handle string,
) (*api.BackgroundProcessStopResult, error) {
	if m == nil || m.projects == nil {
		return nil, fmt.Errorf("project registry not configured")
	}
	projectID = strings.TrimSpace(projectID)
	handle = strings.TrimSpace(handle)
	if projectID == "" {
		return nil, project.ErrNotFound
	}
	if _, err := m.projects.Get(ctx, projectID); err != nil {
		return nil, err
	}
	if m.processes == nil {
		return nil, bgprocess.ErrProcessNotFound
	}
	return m.processes.Lifecycle.StopProjectProcess(projectID, handle, bgprocess.TerminationStoppedByUser)
}

// StopProjectBackground stops and removes all background processes in projectID for project deletion.
func (m *Service) StopProjectBackground(ctx context.Context, projectID string) error {
	if m == nil || m.processes == nil {
		return nil
	}
	return m.processes.Lifecycle.DisposeProject(ctx, strings.TrimSpace(projectID), bgprocess.TerminationProjectDeleted)
}

// AllRunningBackgroundProcesses returns every live background command across all active projects.
func (m *Service) AllRunningBackgroundProcesses(ctx context.Context) ([]api.ProjectBackgroundProcess, error) {
	if m == nil || m.processes == nil {
		return nil, nil
	}
	rows := m.processes.AllRunningBackgroundProcesses(ctx)
	if len(rows) == 0 {
		return nil, nil
	}
	rootsCache := make(map[string][]projectroot.RootRef)
	sessionCache := make(map[string]hydratedSessionMeta, len(rows))
	items := make([]api.ProjectBackgroundProcess, 0, len(rows))
	for _, row := range rows {
		roots, ok := rootsCache[row.ProjectID]
		if !ok && m.projects != nil && row.ProjectID != "" {
			if p, err := m.projects.Get(ctx, row.ProjectID); err == nil && p != nil {
				roots = project.RootRefsFrom(p)
			}
			rootsCache[row.ProjectID] = roots
		}
		items = append(items, m.hydrateProjectProcess(ctx, row, roots, sessionCache))
	}
	return items, nil
}
