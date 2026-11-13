package projectcontrol

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectroot"
)

// ReassignSessionsOffEditorOnlyRoots moves each chat whose active root agents
// cannot use to the agents' primary, as detaching that root would. Admission
// follows host facts, which can change between runs.
func (m *Service) ReassignSessionsOffEditorOnlyRoots(ctx context.Context) error {
	return m.reassignOffEditorOnlyRoots(ctx, func(string) bool { return true })
}

// ReassignProjectSessionsOffEditorOnlyRoots applies the same move to one
// project after its roots change in place.
func (m *Service) ReassignProjectSessionsOffEditorOnlyRoots(ctx context.Context, projectID string) error {
	projectID = strings.TrimSpace(projectID)
	return m.reassignOffEditorOnlyRoots(ctx, func(id string) bool { return id == projectID })
}

func (m *Service) reassignOffEditorOnlyRoots(ctx context.Context, inScope func(projectID string) bool) error {
	if m == nil || m.store == nil || m.projects == nil {
		return nil
	}
	sessions, err := m.store.List(ctx)
	if err != nil {
		return err
	}
	type move struct{ projectID, rootID string }
	done := make(map[move]struct{})
	projects := make(map[string]*project.Project)
	for _, sess := range sessions {
		if sess == nil {
			continue
		}
		projectID, rootID := strings.TrimSpace(sess.ProjectID), strings.TrimSpace(sess.WorkspaceRootID)
		if projectID == "" || rootID == "" || !inScope(projectID) {
			continue
		}
		if _, ok := done[move{projectID, rootID}]; ok {
			continue
		}
		p, ok := projects[projectID]
		if !ok {
			p, err = m.projects.Get(ctx, projectID)
			if err != nil {
				p = nil
			}
			projects[projectID] = p
		}
		if p == nil {
			continue
		}
		admitted := project.RootRefsFrom(p)
		if _, ok := projectroot.RootRefByID(admitted, rootID); ok {
			continue
		}
		if !attachedRoot(p, rootID) {
			continue
		}
		replacement := ""
		if len(admitted) > 0 {
			replacement = admitted[0].ID
		}
		if err := m.store.ReassignSessionsWorkspaceRoot(ctx, projectID, rootID, replacement); err != nil {
			return err
		}
		done[move{projectID, rootID}] = struct{}{}
	}
	return nil
}

func attachedRoot(p *project.Project, rootID string) bool {
	for _, root := range p.Roots {
		if root.ID == rootID {
			return true
		}
	}
	return false
}
