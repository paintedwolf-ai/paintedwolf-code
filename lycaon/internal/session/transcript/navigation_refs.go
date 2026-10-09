package transcript

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/navigationref"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/workspace"
	"github.com/lycaon/lycaon/pkg/api"
)

func (m *Service) NavigationRefs(ctx context.Context, sessionID string, msgs []api.Message) {
	if m == nil || m.store == nil || m.projects == nil || len(msgs) == 0 {
		return
	}
	needsProject := false
	for i := range msgs {
		if messageNeedsNavigationRefs(msgs[i]) {
			needsProject = true
			break
		}
	}
	if !needsProject {
		return
	}
	sess, err := m.store.Get(ctx, sessionID)
	if err != nil || sess == nil || strings.TrimSpace(sess.ProjectID) == "" {
		return
	}
	p, err := m.projects.Get(ctx, sess.ProjectID)
	if err != nil || p == nil {
		return
	}
	aliases := project.RootRefsFrom(p)
	if binding, bound, bindErr := m.Workspace.Binding(ctx, sess); bindErr == nil && bound {
		aliases = append(aliases, project.SubstituteWorktreeRoots(aliases, binding)...)
	}
	for i := range msgs {
		if messageNeedsNavigationRefs(msgs[i]) {
			contextProject := *p
			contextProject.Roots = navigationRootAliases(aliases)
			if m.workerQueue != nil && msgs[i].WorkerID != "" {
				if task, ok := m.workerQueue.Get(msgs[i].WorkerID); ok && task != nil && task.ProjectID == p.ID && task.EffectiveScope().IsWrite() {
					if branchRoots, err := workspace.BranchRootAddresses(task.WorkspaceRoot); err == nil {
						contextProject.Roots = append(contextProject.Roots, navigationRootAliases(branchRoots)...)
					}
				}
			}
			msgs[i].NavigationRefs = navigationref.BuildProjectPathReferences(&contextProject, msgs[i].Content, msgs[i].SourceContext)
		}
	}
}

func navigationRootAliases(refs []projectroot.RootRef) []project.Root {
	roots := make([]project.Root, 0, len(refs))
	for _, ref := range refs {
		roots = append(roots, project.Root{ID: ref.ID, Label: ref.Label, Path: ref.Path, IsPrimary: ref.IsPrimary})
	}
	return roots
}

func messageNeedsNavigationRefs(msg api.Message) bool {
	visibility := msg.Visibility
	if visibility == "" {
		visibility = api.MessageVisibilityTranscript
	}
	return msg.NavigationRefs == nil &&
		msg.Role == api.MessageRoleAssistant &&
		visibility == api.MessageVisibilityTranscript &&
		strings.TrimSpace(msg.Content) != ""
}
