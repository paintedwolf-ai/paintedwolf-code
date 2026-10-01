package session

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/pkg/api"
)

// RootDependentWorker is an in-flight worker job bound to a folder root.
type RootDependentWorker struct {
	JobID     string           `json:"job_id"`
	SessionID string           `json:"session_id"`
	AgentType string           `json:"agent_type,omitempty"`
	Status    api.WorkerStatus `json:"status"`
}

// RootDependentOverlay is a completed write overlay awaiting promote/reject on a root.
type RootDependentOverlay struct {
	OverlayID string `json:"overlay_id"`
	JobID     string `json:"job_id"`
	SessionID string `json:"session_id"`
}

// RootDependentSession is a busy coordinator session bound to a root mid-turn.
type RootDependentSession struct {
	SessionID string            `json:"session_id"`
	Status    api.SessionStatus `json:"status"`
}

// RootDependentDocument is an open or unsaved editor document under a project root.
type RootDependentDocument struct {
	DocumentID string `json:"document_id"`
	Path       string `json:"path"`
}

// RootDependents inventories project-scoped sandbox-bound work that breaks when a folder vanishes.
type RootDependents struct {
	Workers   []RootDependentWorker   `json:"workers"`
	Overlays  []RootDependentOverlay  `json:"overlays"`
	Sessions  []RootDependentSession  `json:"sessions"`
	Documents []RootDependentDocument `json:"documents"`
}

func (d RootDependents) HasAny() bool {
	return len(d.Workers) > 0 || len(d.Overlays) > 0 || len(d.Sessions) > 0 || len(d.Documents) > 0
}

// Details returns the enumerated dependents for structured root_busy rejects.
func (d RootDependents) Details() map[string]any {
	workers := make([]map[string]any, 0, len(d.Workers))
	for _, w := range d.Workers {
		workers = append(workers, map[string]any{
			"job_id":     w.JobID,
			"session_id": w.SessionID,
			"agent_type": w.AgentType,
			"status":     string(w.Status),
		})
	}
	overlays := make([]map[string]any, 0, len(d.Overlays))
	for _, o := range d.Overlays {
		overlays = append(overlays, map[string]any{
			"overlay_id": o.OverlayID,
			"job_id":     o.JobID,
			"session_id": o.SessionID,
		})
	}
	sessions := make([]map[string]any, 0, len(d.Sessions))
	for _, s := range d.Sessions {
		sessions = append(sessions, map[string]any{
			"session_id": s.SessionID,
			"status":     string(s.Status),
		})
	}
	documents := make([]map[string]any, 0, len(d.Documents))
	for _, d := range d.Documents {
		documents = append(documents, map[string]any{
			"document_id": d.DocumentID,
			"path":        d.Path,
		})
	}
	return map[string]any{
		"workers":   workers,
		"overlays":  overlays,
		"sessions":  sessions,
		"documents": documents,
	}
}

func (d RootDependents) merge(other RootDependents) RootDependents {
	seenWorker := map[string]struct{}{}
	seenOverlay := map[string]struct{}{}
	seenSession := map[string]struct{}{}
	seenDocument := map[string]struct{}{}
	out := RootDependents{}
	appendUnique := func() {
		for _, w := range d.Workers {
			if _, ok := seenWorker[w.JobID]; ok {
				continue
			}
			seenWorker[w.JobID] = struct{}{}
			out.Workers = append(out.Workers, w)
		}
		for _, w := range other.Workers {
			if _, ok := seenWorker[w.JobID]; ok {
				continue
			}
			seenWorker[w.JobID] = struct{}{}
			out.Workers = append(out.Workers, w)
		}
		for _, o := range d.Overlays {
			if _, ok := seenOverlay[o.JobID]; ok {
				continue
			}
			seenOverlay[o.JobID] = struct{}{}
			out.Overlays = append(out.Overlays, o)
		}
		for _, o := range other.Overlays {
			if _, ok := seenOverlay[o.JobID]; ok {
				continue
			}
			seenOverlay[o.JobID] = struct{}{}
			out.Overlays = append(out.Overlays, o)
		}
		for _, s := range d.Sessions {
			if _, ok := seenSession[s.SessionID]; ok {
				continue
			}
			seenSession[s.SessionID] = struct{}{}
			out.Sessions = append(out.Sessions, s)
		}
		for _, s := range other.Sessions {
			if _, ok := seenSession[s.SessionID]; ok {
				continue
			}
			seenSession[s.SessionID] = struct{}{}
			out.Sessions = append(out.Sessions, s)
		}
		for _, document := range append(d.Documents, other.Documents...) {
			if _, ok := seenDocument[document.DocumentID]; ok {
				continue
			}
			seenDocument[document.DocumentID] = struct{}{}
			out.Documents = append(out.Documents, document)
		}
	}
	appendUnique()
	return out
}

type projectWorkerLister interface {
	List(ctx context.Context, projectID string, status ...api.WorkerStatus) ([]api.WorkerTask, error)
}

// RootDependents returns sandbox-bound work that would break if rootID were removed.
func (m *Manager) RootDependents(ctx context.Context, projectID, rootID string) (RootDependents, error) {
	if m == nil {
		return RootDependents{}, fmt.Errorf("session manager not configured")
	}
	projectID = strings.TrimSpace(projectID)
	rootID = strings.TrimSpace(rootID)
	if projectID == "" || rootID == "" {
		return RootDependents{}, fmt.Errorf("project_id and root_id required")
	}
	if m.projects == nil {
		return RootDependents{}, fmt.Errorf("project registry not configured")
	}
	p, err := m.projects.Get(ctx, projectID)
	if err != nil {
		return RootDependents{}, err
	}
	roots := project.RootRefsFrom(p)
	detached, ok := projectroot.RootRefByID(roots, rootID)
	if !ok {
		return RootDependents{}, fmt.Errorf("%w: %s", project.ErrRootNotFound, rootID)
	}
	return m.rootDependentsFor(ctx, projectID, detached, roots)
}

// ProjectDependents aggregates dependents across every root on the project (delete / full stop).
func (m *Manager) ProjectDependents(ctx context.Context, projectID string) (RootDependents, error) {
	if m == nil || m.projects == nil {
		return RootDependents{}, fmt.Errorf("project registry not configured")
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return RootDependents{}, fmt.Errorf("project_id required")
	}
	p, err := m.projects.Get(ctx, projectID)
	if err != nil {
		return RootDependents{}, err
	}
	roots := project.RootRefsFrom(p)
	var all RootDependents
	for _, root := range roots {
		dep, err := m.rootDependentsFor(ctx, projectID, root, roots)
		if err != nil {
			return RootDependents{}, err
		}
		all = all.merge(dep)
	}
	return all, nil
}

func (m *Manager) rootDependentsFor(ctx context.Context, projectID string, detached projectroot.RootRef, projectRoots []projectroot.RootRef) (RootDependents, error) {
	var out RootDependents
	if lister, ok := m.workerQueue.(projectWorkerLister); ok && lister != nil {
		inFlight, err := lister.List(ctx, projectID,
			api.WorkerStatusPending, api.WorkerStatusRunning, api.WorkerStatusHeld)
		if err != nil {
			return RootDependents{}, err
		}
		for _, task := range inFlight {
			if !projectroot.WorkerTaskBindsRoot(task, detached, projectRoots) {
				continue
			}
			out.Workers = append(out.Workers, RootDependentWorker{
				JobID:     task.ID,
				SessionID: task.ParentSessionID,
				AgentType: task.AgentType,
				Status:    task.Status,
			})
		}
		complete, err := lister.List(ctx, projectID, api.WorkerStatusComplete)
		if err != nil {
			return RootDependents{}, err
		}
		for _, task := range complete {
			if !task.EffectiveScope().IsWrite() || strings.TrimSpace(task.WorkspaceRoot) == "" {
				continue
			}
			if task.MergeStatus != api.WorkerMergeStatusPending {
				continue
			}
			if !projectroot.WorkerTaskBindsRoot(task, detached, projectRoots) {
				continue
			}
			overlayID := strings.TrimSpace(task.OverlayID)
			if overlayID == "" {
				overlayID = task.ID
			}
			out.Overlays = append(out.Overlays, RootDependentOverlay{
				OverlayID: overlayID,
				JobID:     task.ID,
				SessionID: task.ParentSessionID,
			})
		}
	}
	if m.store != nil {
		sessions, err := m.store.List(ctx)
		if err != nil {
			return RootDependents{}, err
		}
		for _, sess := range sessions {
			if sess == nil || sess.ProjectID != projectID {
				continue
			}
			if len(projectRoots) <= 1 && strings.TrimSpace(sess.WorkspaceRootID) != detached.ID {
				continue
			}
			if sess.Status != api.SessionStatusBusy {
				continue
			}
			out.Sessions = append(out.Sessions, RootDependentSession{
				SessionID: sess.ID,
				Status:    sess.Status,
			})
		}
	}
	return out, nil
}
