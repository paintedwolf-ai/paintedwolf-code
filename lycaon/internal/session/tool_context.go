package session

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/agentpresence"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sourceeffect"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func (m *Manager) buildToolContext(ctx context.Context, sess *api.Session, profileID string, machine inject.Machine) (tools.ToolContext, error) {
	tctx := tools.ToolContext{
		ProjectID:           "",
		SessionID:           "",
		Agent:               profileID,
		ToolAccess:          m.ResolveToolAccess(ctx, sess),
		ActiveRootID:        "",
		SourceWorkspaceKind: api.SourceWorkspaceKindProject,
	}
	if sess != nil {
		tctx.SessionID = sess.ID
		tctx.ParentSessionID = sess.ParentSessionID
		if m != nil && m.store != nil {
			tctx.RootSessionID = RootSessionID(ctx, m.store, sess.ID)
		}
		if tctx.RootSessionID == "" {
			tctx.RootSessionID = sess.ID
		}
		tctx.ProjectID = sess.ProjectID
		tctx.ActiveRootID = sess.WorkspaceRootID
		// Context rebuilds read the same turn ordinal the ledger records.
		if m != nil && m.store != nil {
			if turn, err := m.store.UserTurnOrdinal(ctx, sess.ID); err == nil {
				tctx.UserTurn = turn
			}
		}
	}
	if m != nil && m.loopbackProv != nil {
		tctx.ContainerRecorder = m.loopbackProv
	}
	if m != nil && m.projects != nil && tctx.ProjectID != "" {
		roots, err := m.sessionRootRefs(ctx, sess)
		if err != nil {
			return tools.ToolContext{}, err
		}
		tctx.Roots = roots
		binding, bound, err := m.worktreeBindingFor(ctx, sess)
		if err != nil {
			return tools.ToolContext{}, err
		}
		if bound {
			base, err := m.projects.Get(ctx, tctx.ProjectID)
			if err != nil {
				return tools.ToolContext{}, err
			}
			workspace := project.WithWorktree(base, binding)
			tctx.ProjectSourceBranch = workspace.SourceBranch
			tctx.ProjectRootBranches = workspace.RootBranches
		}

	}
	if len(tctx.Roots) > 0 && tctx.ActiveRootID == "" {
		if r, err := projectroot.PrimaryRoot(tctx.Roots); err == nil {
			tctx.ActiveRootID = r.ID
		}
	}
	if m != nil && tctx.ProjectID != "" {
		tctx.HostDataDir = m.HostDataDirFor(tctx.ProjectID)
	}
	if m != nil {
		tctx.MaxToolSpillBytes = m.effectiveLimits(ctx, sess).MaxToolSpillBytes
		tctx.MutationRecorder = m
		tctx.SourceLedger = m.sourceLedger
		tctx.SourceMutations = m.sourceMutations
		tctx.EditorDocuments = m.editorDocuments
		tctx.CredentialFiles = m.credentialFiles
		tctx.SessionScratchDir = m.sessionScratchDir(ctx, sess)
	}
	attachRepoSizeFact(ctx, m, &tctx)
	attachSkillReadRoots(ctx, m, sess, profileID, machine, &tctx)
	return tctx, nil
}

// SetSourceLedger wires the app-scoped source mutation recorder into every tool invocation.
func (m *Manager) SetSourceLedger(recorder sourceledger.Recorder) {
	if m != nil {
		m.sourceLedger = recorder
	}
}

// SetEditorDocuments wires the open-document view into every tool invocation,
// so a file the person has open is read and written as that document.
func (m *Manager) SetEditorDocuments(documents tools.EditorDocuments) {
	if m != nil {
		m.editorDocuments = documents
	}
}

// SetAgentPresence installs the projection tool calls report file activity to.
func (m *Manager) SetAgentPresence(tracker *agentpresence.Tracker) {
	if m != nil {
		m.agentPresence = tracker
	}
}

// attachSkillReadRoots derives confinement read roots from the compiled machine or effective skills.
func attachSkillReadRoots(
	ctx context.Context,
	m *Manager,
	sess *api.Session,
	profileID string,
	machine inject.Machine,
	tctx *tools.ToolContext,
) {
	if m == nil || tctx == nil {
		return
	}
	if machine.Compiled() {
		tctx.ReadRoots = append([]string(nil), machine.ReadRoots...)
		return
	}
	roots := make([]string, 0, len(tctx.Roots))
	for _, r := range tctx.Roots {
		if path := strings.TrimSpace(r.Path); path != "" {
			roots = append(roots, path)
		}
	}
	loaded, _ := m.EffectiveSkillsForProfile(ctx, sess, profileID, roots)
	if len(loaded) == 0 {
		return
	}
	tctx.ReadRoots = skillReadRoots(loaded)
}

// attachRepoSizeFact reads the progressive brief without waiting; unavailable counts stay unknown.
func attachRepoSizeFact(ctx context.Context, m *Manager, tctx *tools.ToolContext) {
	if m == nil || tctx == nil || m.repoProvider == nil {
		return
	}
	root := tctx.ActiveRootPath()
	if root == "" {
		return
	}
	brief, err := m.repoProvider.Brief(ctx, root)
	if err != nil || brief == nil {
		return
	}
	tctx.RepoFileCount = brief.FileCount
	tctx.RepoFileCountKnown = true
	if len(brief.Layout.TopLevel) > 0 {
		tctx.RepoTopLevel = append([]string(nil), brief.Layout.TopLevel...)
	}
}

// ProjectRootRefs exposes folder roots for worker citation path resolution.
func (m *Manager) ProjectRootRefs(ctx context.Context, projectID string) []projectroot.RootRef {
	if m == nil || m.projects == nil || projectID == "" {
		return nil
	}
	p, err := m.projects.Get(ctx, projectID)
	if err != nil || p == nil {
		return nil
	}
	return project.RootRefsFrom(p)
}

func (m *Manager) sessionRootRefs(ctx context.Context, sess *api.Session) ([]projectroot.RootRef, error) {
	if m == nil || sess == nil {
		return nil, nil
	}
	refs := m.ProjectRootRefs(ctx, sess.ProjectID)
	binding, bound, err := m.worktreeBindingFor(ctx, sess)
	if err != nil {
		return nil, err
	}
	if !bound {
		return refs, nil
	}
	if err := git.NewManager().ValidateWorktree(ctx, binding.Toplevel, binding.WorktreePath, binding.Branch); err != nil {
		return nil, ErrSessionWorktreeStale
	}
	return project.SubstituteWorktreeRoots(refs, binding), nil
}

func (m *Manager) sessionRootPaths(ctx context.Context, sess *api.Session) ([]string, error) {
	refs, err := m.sessionRootRefs(ctx, sess)
	if err != nil {
		return nil, err
	}
	return rootPathsFromRefs(refs), nil
}

func (m *Manager) sessionActiveRootPath(ctx context.Context, sess *api.Session) (string, error) {
	if sess == nil {
		return "", nil
	}
	refs, err := m.sessionRootRefs(ctx, sess)
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

func (m *Manager) projectRootCount(ctx context.Context, sess *api.Session) int {
	if sess == nil {
		return 0
	}
	return len(m.ProjectRootRefs(ctx, sess.ProjectID))
}

// SetSourceMutations shares native effect journaling with project mutation recovery.
func (m *Manager) SetSourceMutations(service sourceeffect.Journal) {
	if m != nil {
		m.sourceMutations = service
	}
}
