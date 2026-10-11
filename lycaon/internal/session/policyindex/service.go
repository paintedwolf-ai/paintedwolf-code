package policyindex

import (
	"context"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/governance"
	"github.com/lycaon/lycaon/internal/projectcontrib"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/scopedstore"
	"github.com/lycaon/lycaon/internal/sourceref"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
	"golang.org/x/sync/singleflight"
)

type Store interface {
	Get(context.Context, string) (*api.Session, error)
}

type Roots interface {
	Applies(context.Context, string, *api.Session) bool
	ActivePath(context.Context, *api.Session) (string, error)
	ProjectRoots(context.Context, string) []projectroot.RootRef
}

type Service struct {
	store     Store
	roots     Roots
	renderer  prompts.PromptTemplateEngine
	cache     scopedstore.LRU[*governance.AgentsMDSessionState]
	warm      singleflight.Group
	listIndex func(string) ([]governance.ResolvedAgentsMD, error)
}

func New(store Store, roots Roots) *Service {
	return &Service{store: store, roots: roots}
}

func (s *Service) SetRenderer(renderer prompts.PromptTemplateEngine) {
	s.renderer = renderer
}

func (s *Service) Forget(sessionID string) {
	if s != nil {
		s.cache.Delete(sessionID)
	}
}

var (
	agentsMDInjectMaxBodyOnce sync.Once
	agentsMDInjectMaxBody     int
)

func (m *Service) ensureState(ctx context.Context, sessionID, workspacePath string) *governance.AgentsMDSessionState {
	if m == nil || strings.TrimSpace(sessionID) == "" {
		return &governance.AgentsMDSessionState{}
	}
	workspacePath = strings.TrimSpace(workspacePath)
	if state, ok := m.cache.Load(sessionID); ok && state != nil && state.RootPath == workspacePath && (state.Indexed() || workspacePath == "") {
		return state
	}
	// Callers share one build, including retries of an unfinished index, so no
	// single caller's cancellation stops it.
	shared := context.WithoutCancel(ctx)
	value, _, _ := m.warm.Do(sessionID+"\x00"+workspacePath, func() (any, error) {
		state, _ := m.cache.Load(sessionID)
		if state == nil || state.RootPath != workspacePath {
			state = &governance.AgentsMDSessionState{RootPath: workspacePath}
		}
		if !state.Indexed() && workspacePath != "" {
			m.buildIndex(shared, state, workspacePath)
		}
		m.cache.Store(sessionID, state)
		return state, nil
	})
	return value.(*governance.AgentsMDSessionState)
}

func (m *Service) buildIndex(ctx context.Context, state *governance.AgentsMDSessionState, workspacePath string) {
	workspacePath = strings.TrimSpace(workspacePath)
	if state == nil || workspacePath == "" {
		return
	}
	index, err := m.loadIndex(ctx, workspacePath)
	if err != nil {
		return
	}
	state.SetIndex(index)
}

func (m *Service) loadIndex(ctx context.Context, workspacePath string) ([]governance.ResolvedAgentsMD, error) {
	if m.listIndex != nil {
		return m.listIndex(workspacePath)
	}
	return governance.ListIndex(ctx, workspacePath)
}

// Warm caches AGENTS.md index metadata for a session workspace.
func (m *Service) Warm(ctx context.Context, sess *api.Session) {
	if m == nil || sess == nil {
		return
	}
	if !m.roots.Applies(ctx, projectcontrib.SurfaceAgentsMD, sess) {
		return
	}
	workspacePath, err := m.roots.ActivePath(ctx, sess)
	if err != nil {
		return
	}
	m.ensureState(ctx, sess.ID, workspacePath)
}

func (m *Service) Index(ctx context.Context, sess *api.Session) (api.Message, bool) {
	if m == nil || m.renderer == nil || sess == nil {
		return api.Message{}, false
	}
	if !m.roots.Applies(ctx, projectcontrib.SurfaceAgentsMD, sess) {
		return api.Message{}, false
	}
	workspacePath, err := m.roots.ActivePath(ctx, sess)
	if err != nil {
		return api.Message{}, false
	}
	state := m.ensureState(ctx, sess.ID, workspacePath)
	index := state.Snapshot()
	if len(index) == 0 {
		return api.Message{}, false
	}
	renderer := prompts.NewInjectRenderer(m.renderer)
	block, err := governance.BuildIndexInject(ctx, renderer, sess.ID, index)
	if err != nil || strings.TrimSpace(block.Content) == "" {
		return api.Message{}, false
	}
	return m.message(ctx, sess, block), true
}

func (m *Service) maxBodyBytes() int {
	agentsMDInjectMaxBodyOnce.Do(func() {
		agentsMDInjectMaxBody = governance.DefaultAgentsMDInjectMaxBodyBytes
		root := ""
		if m != nil {
			if fe, ok := m.renderer.(*prompts.FileTemplateEngine); ok && fe != nil {
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

func (m *Service) Chain(ctx context.Context, sess *api.Session, relPath string) (api.Message, error) {
	if m == nil || m.renderer == nil || sess == nil {
		return api.Message{}, nil
	}
	if !m.roots.Applies(ctx, projectcontrib.SurfaceAgentsMD, sess) {
		return api.Message{}, nil
	}
	relPath = strings.TrimSpace(relPath)
	if relPath == "" {
		return api.Message{}, nil
	}
	workspacePath, err := m.roots.ActivePath(ctx, sess)
	if err != nil {
		return api.Message{}, err
	}
	workspacePath = strings.TrimSpace(workspacePath)
	if workspacePath == "" {
		return api.Message{}, nil
	}
	renderer := prompts.NewInjectRenderer(m.renderer)
	block, err := governance.BuildChainInject(ctx, renderer, sess.ID, workspacePath, relPath, m.maxBodyBytes())
	if err != nil {
		return api.Message{}, err
	}
	return m.message(ctx, sess, block), nil
}

// ForPaths renders the policy chain for suggested task paths.
func (m *Service) ForPaths(ctx context.Context, sessionID, workspacePath string, paths []string) (api.Message, error) {
	sess := &api.Session{ID: sessionID, WorkspacePath: workspacePath}
	if m.store != nil {
		if loaded, err := m.store.Get(ctx, sessionID); err == nil && loaded != nil {
			sess = loaded
		}
	}
	if !m.roots.Applies(ctx, projectcontrib.SurfaceAgentsMD, sess) {
		return api.Message{}, nil
	}
	relPath := governance.FirstConcreteScopePath(paths)
	if relPath == "" {
		return api.Message{}, nil
	}
	return m.Chain(ctx, sess, relPath)
}

func (m *Service) message(ctx context.Context, sess *api.Session, block governance.AgentsMDInject) api.Message {
	msg := api.Message{Role: api.MessageRoleSystem, Content: block.Content, Origin: api.MessageOriginProject, Authority: api.ContentAuthorityDeveloper, TrustTier: api.ContentTrustTierTrusted}
	roots := m.roots.ProjectRoots(ctx, sess.ProjectID)
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
