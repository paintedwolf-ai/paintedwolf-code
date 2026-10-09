package scope

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/noticeerr"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectcontrib"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/session/store"
	sessiontree "github.com/lycaon/lycaon/internal/session/tree"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/pkg/api"
)

type Store interface {
	sessiontree.Reader
	GetWorktreeBinding(context.Context, string) (*store.WorktreeBinding, bool, error)
}

type Projects interface {
	Get(context.Context, string) (*project.Project, error)
}

type Service struct {
	DataDir  string
	store    Store
	projects Projects
	trust    *settings.TrustSurfacesStore
}

func New(store Store) *Service                                 { return &Service{store: store} }
func (m *Service) SetProjects(projects Projects)               { m.projects = projects }
func (m *Service) SetTrust(trust *settings.TrustSurfacesStore) { m.trust = trust }

var ErrWorktreeStale error = noticeerr.NewSentinel("this chat's worktree is missing or invalid", api.NoticeCodeWorktreeStale)

func (m *Service) Binding(ctx context.Context, sess *api.Session) (project.Binding, bool, error) {
	if m == nil || m.store == nil || sess == nil {
		return project.Binding{}, false, nil
	}
	bindingSessionID := strings.TrimSpace(sess.ID)
	if sess.IsWorkerChild() {
		bindingSessionID = sessiontree.RootID(ctx, m.store, bindingSessionID)
	}
	row, ok, err := m.store.GetWorktreeBinding(ctx, bindingSessionID)
	if err != nil {
		return project.Binding{}, false, err
	}
	if !ok || row == nil {
		return project.Binding{}, false, nil
	}
	return project.Binding{
		ID:           row.WorktreeID,
		Toplevel:     row.Toplevel,
		WorktreePath: row.WorktreePath,
		Branch:       row.Branch,
		BaseBranch:   row.BaseBranch,
	}, true, nil
}

func (m *Service) CheckReady(ctx context.Context, sessionID string) error {
	if m == nil || m.store == nil {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	sess, err := m.store.Get(ctx, sessionID)
	if err != nil {
		return err
	}
	b, bound, err := m.Binding(ctx, sess)
	if err != nil {
		return err
	}
	if !bound {
		return nil
	}
	if err := git.NewManager().ValidateWorktree(ctx, b.Toplevel, b.WorktreePath, b.Branch); err != nil {
		return ErrWorktreeStale
	}
	return nil
}

func (m *Service) ProjectRoots(ctx context.Context, projectID string) []projectroot.RootRef {
	if m == nil || m.projects == nil || projectID == "" {
		return nil
	}
	p, err := m.projects.Get(ctx, projectID)
	if err != nil || p == nil {
		return nil
	}
	return project.RootRefsFrom(p)
}

func (m *Service) Roots(ctx context.Context, sess *api.Session) ([]projectroot.RootRef, error) {
	if m == nil || sess == nil {
		return nil, nil
	}
	refs := m.ProjectRoots(ctx, sess.ProjectID)
	binding, bound, err := m.Binding(ctx, sess)
	if err != nil {
		return nil, err
	}
	if !bound {
		return refs, nil
	}
	if err := git.NewManager().ValidateWorktree(ctx, binding.Toplevel, binding.WorktreePath, binding.Branch); err != nil {
		return nil, ErrWorktreeStale
	}
	return project.SubstituteWorktreeRoots(refs, binding), nil
}

func (m *Service) Paths(ctx context.Context, sess *api.Session) ([]string, error) {
	refs, err := m.Roots(ctx, sess)
	if err != nil {
		return nil, err
	}
	return rootPathsFromRefs(refs), nil
}

func (m *Service) ActivePath(ctx context.Context, sess *api.Session) (string, error) {
	if sess == nil {
		return "", nil
	}
	refs, err := m.Roots(ctx, sess)
	if err != nil {
		return "", err
	}
	if root, err := projectroot.ActiveRoot(refs, sess.WorkspaceRootID); err == nil {
		return strings.TrimSpace(root.Path), nil
	}
	if root, err := projectroot.PrimaryRoot(refs); err == nil {
		return strings.TrimSpace(root.Path), nil
	}
	return "", nil
}

func rootPathsFromRefs(refs []projectroot.RootRef) []string {
	if len(refs) == 0 {
		return nil
	}
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		if path := strings.TrimSpace(r.Path); path != "" {
			out = append(out, path)
		}
	}
	return out
}

func (m *Service) RootCount(ctx context.Context, sess *api.Session) int {
	if sess == nil {
		return 0
	}
	return len(m.ProjectRoots(ctx, sess.ProjectID))
}

func (m *Service) Project(ctx context.Context, sess *api.Session) (*project.Project, bool) {
	if m == nil || m.projects == nil || sess == nil {
		return nil, false
	}
	projectID := strings.TrimSpace(sess.ProjectID)
	if projectID == "" {
		return nil, false
	}
	p, err := m.projects.Get(ctx, projectID)
	if err != nil || p == nil {
		return nil, false
	}
	return p, true
}

func (m *Service) Applies(ctx context.Context, surface string, sess *api.Session) bool {
	if m == nil || m.trust == nil {
		return false
	}
	p, ok := m.Project(ctx, sess)
	if !ok {
		return false
	}
	return m.trust.Applies(string(surface), *p)
}

func (m *Service) SettingsPath(ctx context.Context, sess *api.Session) string {
	if m == nil || sess == nil {
		return ""
	}
	if !m.Applies(ctx, projectcontrib.SurfaceProjectSettings, sess) {
		return ""
	}
	projectDir, err := m.ActivePath(ctx, sess)
	if err != nil {
		return ""
	}
	return projectDir
}

func (m *Service) SettingsRoots(ctx context.Context, sess *api.Session) []string {
	return m.SurfaceRoots(ctx, projectcontrib.SurfaceProjectSettings, sess)
}

func (m *Service) PromptRoots(ctx context.Context, sess *api.Session) []string {
	return m.SurfaceRoots(ctx, projectcontrib.SurfacePromptOverrides, sess)
}

func (m *Service) SurfaceRoots(ctx context.Context, surface string, sess *api.Session) []string {
	if m == nil || sess == nil {
		return nil
	}
	if !m.Applies(ctx, surface, sess) {
		return nil
	}
	return m.OverlayRoots(ctx, sess)
}

func (m *Service) OverlayRoots(ctx context.Context, sess *api.Session) []string {
	if m == nil || sess == nil {
		return nil
	}
	refs, err := m.Roots(ctx, sess)
	if err != nil {
		return nil
	}
	resolution, err := project.ResolveOverlay(refs, sess.WorkspaceRootID)
	if err != nil {
		return nil
	}
	return resolution.Paths
}

func (m *Service) CheckpointRoot(ctx context.Context, sess *api.Session) string {
	if sess == nil {
		return ""
	}
	if m != nil {
		if root, err := m.ActivePath(ctx, sess); err == nil && root != "" {
			return root
		}
	}
	return strings.TrimSpace(sess.WorkspacePath)
}

func (m *Service) HostDataDir(projectID string) string {
	return project.HostDataDir(m.DataDir, projectID)
}
