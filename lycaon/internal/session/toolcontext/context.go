package toolcontext

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/session/profiles"
	sessiontree "github.com/lycaon/lycaon/internal/session/tree"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func (m *Service) Build(ctx context.Context, sess *api.Session, profileID string, machine inject.Machine) (tools.ToolContext, error) {
	tctx := tools.ToolContext{
		ProjectID:           "",
		SessionID:           "",
		Agent:               profileID,
		ToolAccess:          m.profiles.ResolveToolAccess(ctx, sess),
		ActiveRootID:        "",
		SourceWorkspaceKind: api.SourceWorkspaceKindProject,
	}
	if sess != nil {
		tctx.SessionID = sess.ID
		tctx.ParentSessionID = sess.ParentSessionID
		if m != nil && m.store != nil {
			tctx.RootSessionID = sessiontree.RootID(ctx, m.store, sess.ID)
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
		roots, err := m.workspace.Roots(ctx, sess)
		if err != nil {
			return tools.ToolContext{}, err
		}
		tctx.Roots = roots
		binding, bound, err := m.workspace.Binding(ctx, sess)
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
		tctx.HostDataDir = project.HostDataDir(m.dataDir, tctx.ProjectID)
	}
	if m != nil {
		tctx.MaxToolSpillBytes = m.limits.Effective(ctx, sess).MaxToolSpillBytes
		tctx.MutationRecorder = m.captures
		tctx.SourceLedger = m.SourceLedger
		tctx.SourceMutations = m.sourceMutations
		tctx.EditorDocuments = m.editorDocuments
		tctx.CredentialFiles = m.credentialFiles
		tctx.SessionScratchDir = m.execution.ScratchDir(ctx, sess)
	}
	m.attachRepoSizeFact(ctx, &tctx)
	m.AttachSkillReadRoots(ctx, sess, profileID, machine, &tctx)
	return tctx, nil
}

// attachSkillReadRoots derives confinement read roots from the compiled machine or effective skills.
func (m *Service) AttachSkillReadRoots(
	ctx context.Context,
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
	loaded, _ := m.profiles.EffectiveSkillsForProfile(ctx, sess, profileID, roots)
	if len(loaded) == 0 {
		return
	}
	tctx.ReadRoots = profiles.SkillReadRoots(loaded)
}

// attachRepoSizeFact reads the progressive brief without waiting; unavailable counts stay unknown.
func (m *Service) attachRepoSizeFact(ctx context.Context, tctx *tools.ToolContext) {
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
