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
	tctx := tools.ToolContext{Identity: tools.InvocationIdentity{ProjectID: "",
		SessionID: "",
		Agent:     profileID}, Source: tools.InvocationSource{ActiveRootID: "",
		SourceWorkspaceKind: api.SourceWorkspaceKindProject}, Turn: tools.InvocationTurn{ToolAccess: m.profiles.ResolveToolAccess(ctx, sess)},
	}
	if sess != nil {
		tctx.Identity.SessionID = sess.ID
		tctx.Identity.ParentSessionID = sess.ParentSessionID
		if m != nil && m.store != nil {
			tctx.Identity.RootSessionID = sessiontree.RootID(ctx, m.store, sess.ID)
		}
		if tctx.Identity.RootSessionID == "" {
			tctx.Identity.RootSessionID = sess.ID
		}
		tctx.Identity.ProjectID = sess.ProjectID
		tctx.Source.ActiveRootID = sess.WorkspaceRootID
		// Context rebuilds read the same turn ordinal the ledger records.
		if m != nil && m.store != nil {
			if turn, err := m.store.UserTurnOrdinal(ctx, sess.ID); err == nil {
				tctx.Identity.UserTurn = turn
			}
		}
	}
	if m != nil && m.loopbackProv != nil {
		tctx.Local.ContainerRecorder = m.loopbackProv
	}
	if m != nil && m.projects != nil && tctx.Identity.ProjectID != "" {
		roots, err := m.workspace.Roots(ctx, sess)
		if err != nil {
			return tools.ToolContext{}, err
		}
		tctx.Source.Roots = roots
		binding, bound, err := m.workspace.Binding(ctx, sess)
		if err != nil {
			return tools.ToolContext{}, err
		}
		if bound {
			base, err := m.projects.Get(ctx, tctx.Identity.ProjectID)
			if err != nil {
				return tools.ToolContext{}, err
			}
			workspace := project.WithWorktree(base, binding)
			tctx.Source.ProjectSourceBranch = workspace.SourceBranch
			tctx.Source.ProjectRootBranches = workspace.RootBranches
		}

	}
	if len(tctx.Source.Roots) > 0 && tctx.Source.ActiveRootID == "" {
		if r, err := projectroot.PrimaryRoot(tctx.Source.Roots); err == nil {
			tctx.Source.ActiveRootID = r.ID
		}
	}
	if m != nil && tctx.Identity.ProjectID != "" {
		tctx.Host.HostDataDir = project.HostDataDir(m.dataDir, tctx.Identity.ProjectID)
	}
	if m != nil {
		tctx.Host.MaxToolSpillBytes = m.limits.Effective(ctx, sess).MaxToolSpillBytes
		tctx.Source.MutationRecorder = m.captures
		tctx.Source.SourceLedger = m.SourceLedger
		tctx.Source.History = m.history
		tctx.Source.Commands = m.commands
		tctx.Source.GitMutations = m.gitMutations
		tctx.Source.Observations = m.observations
		tctx.Source.SourceMutations = m.sourceMutations
		tctx.Source.EditorDocuments = m.editorDocuments
		tctx.Effects.CredentialFiles = m.credentialFiles
		tctx.Host.SessionScratchDir = m.execution.ScratchDir(ctx, sess)
	}
	m.attachRepoSizeFact(ctx, &tctx)
	m.AttachSkillReadRoots(ctx, sess, profileID, machine, &tctx)
	return tctx, nil
}

// AttachSkillReadRoots derives confinement read roots from the compiled machine or effective skills.
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
		tctx.Files.ReadRoots = append([]string(nil), machine.ReadRoots...)
		return
	}
	roots := make([]string, 0, len(tctx.Source.Roots))
	for _, r := range tctx.Source.Roots {
		if path := strings.TrimSpace(r.Path); path != "" {
			roots = append(roots, path)
		}
	}
	loaded, _ := m.profiles.EffectiveSkillsForProfile(ctx, sess, profileID, roots)
	if len(loaded) == 0 {
		return
	}
	tctx.Files.ReadRoots = profiles.SkillReadRoots(loaded)
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
	tctx.Source.RepoFileCount = brief.FileCount
	tctx.Source.RepoFileCountKnown = true
	if len(brief.Layout.TopLevel) > 0 {
		tctx.Source.RepoTopLevel = append([]string(nil), brief.Layout.TopLevel...)
	}
}
