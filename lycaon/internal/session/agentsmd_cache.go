package session

import (
	"context"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/governance"
	"github.com/lycaon/lycaon/internal/projectcontrib"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sourceref"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

var (
	agentsMDInjectMaxBodyOnce sync.Once
	agentsMDInjectMaxBody     int
)

func (m *Manager) ensureAgentsMDState(ctx context.Context, sessionID, workspacePath string) *governance.AgentsMDSessionState {
	if m == nil || strings.TrimSpace(sessionID) == "" {
		return &governance.AgentsMDSessionState{}
	}
	workspacePath = strings.TrimSpace(workspacePath)
	if state, ok := m.agentsMDCache.Load(sessionID); ok && state != nil && state.RootPath == workspacePath && (state.Indexed() || workspacePath == "") {
		return state
	}
	// Callers share one build, including retries of an unfinished index, so no
	// single caller's cancellation stops it.
	shared := context.WithoutCancel(ctx)
	value, _, _ := m.agentsMDWarm.Do(sessionID+"\x00"+workspacePath, func() (any, error) {
		state, _ := m.agentsMDCache.Load(sessionID)
		if state == nil || state.RootPath != workspacePath {
			state = &governance.AgentsMDSessionState{RootPath: workspacePath}
		}
		if !state.Indexed() && workspacePath != "" {
			m.buildAgentsMDIndex(shared, state, workspacePath)
		}
		m.agentsMDCache.Store(sessionID, state)
		return state, nil
	})
	return value.(*governance.AgentsMDSessionState)
}

func (m *Manager) buildAgentsMDIndex(ctx context.Context, state *governance.AgentsMDSessionState, workspacePath string) {
	workspacePath = strings.TrimSpace(workspacePath)
	if state == nil || workspacePath == "" {
		return
	}
	index, err := m.listAgentsMDIndex(ctx, workspacePath)
	if err != nil {
		return
	}
	state.SetIndex(index)
}

func (m *Manager) listAgentsMDIndex(ctx context.Context, workspacePath string) ([]governance.ResolvedAgentsMD, error) {
	if m.agentsMDListIndex != nil {
		return m.agentsMDListIndex(workspacePath)
	}
	return governance.ListIndex(ctx, workspacePath)
}

// WarmAgentsMD caches AGENTS.md index metadata for a session workspace.
func (m *Manager) WarmAgentsMD(ctx context.Context, sess *api.Session) {
	if m == nil || sess == nil {
		return
	}
	if !m.surfaceApplies(ctx, projectcontrib.SurfaceAgentsMD, sess) {
		return
	}
	workspacePath, err := m.sessionActiveRootPath(ctx, sess)
	if err != nil {
		return
	}
	m.ensureAgentsMDState(ctx, sess.ID, workspacePath)
}

func (m *Manager) agentsMDIndexInject(ctx context.Context, sess *api.Session) (api.Message, bool) {
	if m == nil || m.prompts == nil || sess == nil {
		return api.Message{}, false
	}
	if !m.surfaceApplies(ctx, projectcontrib.SurfaceAgentsMD, sess) {
		return api.Message{}, false
	}
	workspacePath, err := m.sessionActiveRootPath(ctx, sess)
	if err != nil {
		return api.Message{}, false
	}
	state := m.ensureAgentsMDState(ctx, sess.ID, workspacePath)
	index := state.Snapshot()
	if len(index) == 0 {
		return api.Message{}, false
	}
	renderer := prompts.NewInjectRenderer(m.prompts)
	block, err := governance.BuildIndexInject(ctx, renderer, sess.ID, index)
	if err != nil || strings.TrimSpace(block.Content) == "" {
		return api.Message{}, false
	}
	return m.agentsMDMessage(ctx, sess, block), true
}

func (m *Manager) agentsMDInjectMaxBodyBytes() int {
	agentsMDInjectMaxBodyOnce.Do(func() {
		agentsMDInjectMaxBody = governance.DefaultAgentsMDInjectMaxBodyBytes
		root := ""
		if m != nil {
			if fe, ok := m.prompts.(*prompts.FileTemplateEngine); ok && fe != nil {
				root = strings.TrimSpace(fe.Layers().ModuleRoot)
			}
		}
		if root == "" {
			root = configlayout.FindModuleRoot()
		}
		if root == "" {
			return
		}
		budgets, err := prompts.LoadPromptBudgets()
		if err != nil || budgets == nil || budgets.AgentsMDInject == nil || budgets.AgentsMDInject.MaxBodyBytes <= 0 {
			return
		}
		agentsMDInjectMaxBody = budgets.AgentsMDInject.MaxBodyBytes
	})
	if agentsMDInjectMaxBody <= 0 {
		return governance.DefaultAgentsMDInjectMaxBodyBytes
	}
	return agentsMDInjectMaxBody
}

func (m *Manager) agentsMDChainInject(ctx context.Context, sess *api.Session, relPath string) (api.Message, error) {
	if m == nil || m.prompts == nil || sess == nil {
		return api.Message{}, nil
	}
	if !m.surfaceApplies(ctx, projectcontrib.SurfaceAgentsMD, sess) {
		return api.Message{}, nil
	}
	relPath = strings.TrimSpace(relPath)
	if relPath == "" {
		return api.Message{}, nil
	}
	workspacePath, err := m.sessionActiveRootPath(ctx, sess)
	if err != nil {
		return api.Message{}, err
	}
	workspacePath = strings.TrimSpace(workspacePath)
	if workspacePath == "" {
		return api.Message{}, nil
	}
	renderer := prompts.NewInjectRenderer(m.prompts)
	block, err := governance.BuildChainInject(ctx, renderer, sess.ID, workspacePath, relPath, m.agentsMDInjectMaxBodyBytes())
	if err != nil {
		return api.Message{}, err
	}
	return m.agentsMDMessage(ctx, sess, block), nil
}

// AgentsMDChainForPaths renders the policy chain for suggested task paths.
func (m *Manager) AgentsMDChainForPaths(ctx context.Context, sessionID, workspacePath string, paths []string) (api.Message, error) {
	sess := &api.Session{ID: sessionID, WorkspacePath: workspacePath}
	if m.store != nil {
		if loaded, err := m.store.Get(ctx, sessionID); err == nil && loaded != nil {
			sess = loaded
		}
	}
	if !m.surfaceApplies(ctx, projectcontrib.SurfaceAgentsMD, sess) {
		return api.Message{}, nil
	}
	relPath := governance.FirstConcreteScopePath(paths)
	if relPath == "" {
		return api.Message{}, nil
	}
	return m.agentsMDChainInject(ctx, sess, relPath)
}

func (m *Manager) agentsMDMessage(ctx context.Context, sess *api.Session, block governance.AgentsMDInject) api.Message {
	msg := api.Message{Role: api.MessageRoleSystem, Content: block.Content, Origin: api.MessageOriginProject, Authority: api.ContentAuthorityDeveloper, TrustTier: api.ContentTrustTierTrusted}
	roots := m.ProjectRootRefs(ctx, sess.ProjectID)
	root, err := projectroot.ActiveRoot(roots, sess.WorkspaceRootID)
	if err != nil {
		return msg
	}
	capture := tools.ToolContext{
		Identity: tools.InvocationIdentity{ProjectID: sess.ProjectID},
		Source: tools.InvocationSource{Roots: roots,
			ActiveRootID: root.ID},
		Effects: tools.InvocationEffects{Out: &tools.ToolInvocationOut{}},
	}
	for _, path := range block.Paths {
		capture.RecordSourcePath(path, api.NavigationEntryKindFile)
	}
	msg.SourceContext = sourceref.Mentioned(capture.Effects.Out.SourceContext, msg.Content)
	return msg
}
