package promptsource

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/browser/pagesession"
	"github.com/lycaon/lycaon/internal/coordinator"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/prompts"
	sessionlimits "github.com/lycaon/lycaon/internal/session/limits"
	"github.com/lycaon/lycaon/internal/session/loading"
	"github.com/lycaon/lycaon/internal/session/policyfacts"
	"github.com/lycaon/lycaon/internal/session/processcontrol"
	"github.com/lycaon/lycaon/internal/session/profiles"
	sessionscope "github.com/lycaon/lycaon/internal/session/scope"
	"github.com/lycaon/lycaon/internal/session/toolcontext"
	sessiontree "github.com/lycaon/lycaon/internal/session/tree"
	"github.com/lycaon/lycaon/internal/session/turnguards"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/internal/session/workerworkspace"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/webresearch"
	"github.com/lycaon/lycaon/pkg/api"
)

type ContextRepository interface {
	Get(ctx context.Context, id string) (*api.Session, error)
}
type Context struct {
	Frame       inject.CoordinatorTurnFrameSource
	Guards      *turnguards.Service
	Limits      *sessionlimits.Service
	Loading     *loading.Service
	Pages       *pagesession.Registry
	Policy      *policyfacts.Service
	Processes   *processcontrol.Service
	Profiles    *profiles.Service
	Prompts     prompts.PromptTemplateEngine
	Runtime     *coordinator.Runtime
	Sessions    ContextRepository
	ToolContext *toolcontext.Service
	Tools       tools.ToolRegistry
	WebResearch *webresearch.ConfigStore
	WorkerState *workeroutcomes.State
	Workspace   *sessionscope.Service
	Workspaces  *workerworkspace.Service
}

func (m *Context) Build() promptloop.ContextDeps {
	rt := m.Runtime
	deps := promptloop.ContextDeps{
		Limits:                          m.Limits.Effective,
		CoordinatorSurfaceActivityLabel: surface.LoadCoordinatorSurfaceActivityLabel,
		SetPromptTurnSurface: func(sessionID, surfaceID string) {
			rt.Assembly().SetTurnSurfaceID(sessionID, surfaceID)
		},
		PromptTurnSurface: func(sessionID string) string {
			return rt.PromptTurnSurfaceID(sessionID)
		},
		Tools: m.Tools,
		RootSessionID: func(ctx context.Context, sessionID string) string {
			return sessiontree.RootID(ctx, m.Sessions, sessionID)
		},
		Policy: m.Guards.Policy(),
		CoordinatorPostureRules: func(ctx context.Context, sess *api.Session) ([]string, error) {
			if sess == nil || sess.Posture == "" {
				return nil, nil
			}
			postures, err := m.Profiles.EffectivePostures(ctx, sess)
			if err != nil || postures == nil {
				return nil, err
			}
			paths, err := postures.RulesPaths(sess.Posture)
			if err != nil {
				return nil, err
			}
			return append([]string(nil), paths...), nil
		},
		ProjectRootCount: func(ctx context.Context, sess *api.Session) int {
			_, count, _ := m.Workspace.PromptRootRows(ctx, sess)
			return count
		},
		OverlayRootPaths: func(ctx context.Context, sess *api.Session) []string {
			return m.Workspace.SettingsRoots(ctx, sess)
		},
		LoadedTools:  m.Loading.LoadedTools,
		ToolObserved: m.Loading.ObserveToolCall,
		LiveResources: func(sessionID string) toolcontract.ResourcePresence {
			presence := toolcontract.ResourcePresence{}
			if m != nil && m.Processes.Background != nil {
				presence.CommandJobs = m.Processes.Background.HasPipelineHandles(sessionID)
				presence.Terminals = m.Processes.Background.CountLivePTYs(sessionID) > 0
			}
			if m != nil && m.Pages != nil {
				presence.Pages = m.Pages.CountLive(sessionID) > 0
			}
			if m != nil {
				presence.HeldCalls = m.Processes.Held.HasHandles(sessionID)
			}
			return presence
		},
		BuildMessages:         m.Runtime.BuildCompletionMessages,
		WebSearchEnabled:      m.WebSearchEnabled,
		CoordinatorFrame:      m.Frame,
		ImplementSessionState: m.WorkerState.ForSession,
	}
	deps.RefreshToolContext = func(ctx context.Context, sess *api.Session, machine inject.Machine) (tools.ToolContext, error) {
		profileID := strings.TrimSpace(machine.ProfileID)
		if profileID == "" {
			var err error
			profileID, err = m.Profiles.PromptToolProfile(ctx, sess)
			if err != nil {
				return tools.ToolContext{}, err
			}
		}
		tctx, err := m.ToolContext.Build(ctx, sess, profileID, machine)
		if err != nil {
			return tools.ToolContext{}, err
		}
		return m.Workspaces.Enrich(ctx, sess, tctx)
	}
	deps.MCPAlwaysLoad = func(ctx context.Context, sess *api.Session) map[string]bool {
		if m.Policy.MCP == nil {
			return nil
		}
		return m.Policy.MCP.ToolLoadingModes(ctx, m.Workspace.SettingsPath(ctx, sess))
	}

	if m.Prompts != nil {
		deps.ToolProcedures = m.ToolProcedures
	}

	return deps
}

func (m *Context) ToolProcedures(ctx context.Context, sess *api.Session, profileID string, offered []string) (string, error) {
	return inject.RenderToolProceduresBlock(ctx, prompts.NewInjectRenderer(m.Prompts), sess.ID, profileID, offered, m.Loading.Ledger.Omitted(sess.ID))
}

func (m *Context) WebSearchEnabled() bool {
	return m.WebResearch == nil || m.WebResearch.SearchEnabled()
}
