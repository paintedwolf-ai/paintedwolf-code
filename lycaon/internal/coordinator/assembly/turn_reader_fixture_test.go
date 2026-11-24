package assembly

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolpolicy"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// withTestTurnReader binds the fixture's request projection: the turn
// pipeline over the fixture's tool lister, or the declared surface and
// profile tools when it lists none.
func withTestTurnReader(deps AssemblyDeps) AssemblyDeps {
	if deps.TurnReader != nil {
		return deps
	}
	deps.TurnReader = func(ctx context.Context, sess *api.Session, history []api.Message, frame inject.CoordinatorTurnFrame) (prompts.Reader, error) {
		host, err := fixtureHostFacts(ctx, deps, sess)
		if err != nil {
			return prompts.Reader{}, err
		}
		if deps.PromptToolLister == nil {
			return fixtureDeclaredReader(ctx, deps, host, sess, history, frame)
		}
		loop := promptloop.ContextDeps{
			Policy: toolpolicy.NewEngine(toolpolicy.EngineDeps{
				ToolLister:       fixtureToolLister{sess: sess, list: deps.PromptToolLister},
				ProjectRootCount: func(context.Context, *api.Session) int { return host.RootCount },
			}),
			ImplementSessionState: deps.ImplementSessionState,
			ProjectRootCount:      func(context.Context, *api.Session) int { return host.RootCount },
			HostFacts:             func(context.Context, *api.Session) (prompts.HostFacts, error) { return host, nil },
			ContextSurfaceDeps:    promptloop.ContextSurfaceDeps{WebSearchEnabled: func() bool { return host.WebSearch }},
		}
		if deps.Selection != nil {
			loop.LoadedTools = deps.Selection.LoadedTools
		}
		loop.LiveResources = func(sessionID string) toolcontract.ResourcePresence {
			var live toolcontract.ResourcePresence
			if deps.CommandJobs != nil {
				live.CommandJobs = len(deps.CommandJobs(sessionID)) > 0
			}
			if deps.HeldCalls != nil {
				live.HeldCalls = len(deps.HeldCalls(sessionID)) > 0
			}
			return live
		}
		if strings.TrimSpace(frame.Machine.ProfileID) == "" && sess.IsWorkerChild() {
			return prompts.Reader{}, prompts.ErrUnknownAgent
		}
		return promptloop.TurnReader(ctx, loop, sess, history, frame)
	}
	return deps
}

func fixtureHostFacts(ctx context.Context, deps AssemblyDeps, sess *api.Session) (prompts.HostFacts, error) {
	profiles, err := sandbox.LoadToolProfiles()
	if err != nil {
		return prompts.HostFacts{}, err
	}
	host := prompts.HostFacts{Profiles: profiles, WebSearch: true}
	if deps.SessionView != nil {
		if view := deps.SessionView(ctx, sess); view != nil {
			if len(view.ToolProfiles) > 0 {
				host.Profiles = view.ToolProfiles
			}
			host.Schemas = view.ToolSchemas
		}
	}
	if sess.Mode == api.SessionModeAsk {
		host.SessionMode = string(api.SessionModeAsk)
	}
	if deps.WebSearchEnabled != nil {
		host.WebSearch = deps.WebSearchEnabled()
	}
	if deps.WorkspaceRoots != nil {
		_, host.RootCount, _, _ = deps.WorkspaceRoots(ctx, sess)
	} else if strings.TrimSpace(sess.WorkspacePath) != "" {
		host.RootCount = 1
	}
	if !sess.IsWorkerChild() {
		host.SpawnAllowlist = spawn.AmbientAllowedAgents()
	}
	return host, nil
}

func fixtureDeclaredReader(ctx context.Context, deps AssemblyDeps, host prompts.HostFacts, sess *api.Session, history []api.Message, frame inject.CoordinatorTurnFrame) (prompts.Reader, error) {
	if sess.IsWorkerChild() {
		return prompts.NewReader(host, prompts.TurnProjection{ProfileID: frame.Machine.ProfileID, Declared: true})
	}
	var implState surface.ImplementSessionState
	if deps.ImplementSessionState != nil {
		implState = deps.ImplementSessionState(ctx, sess)
	}
	profile := surface.ResolveTurnProfile(frame.RunContext, sess, history, implState)
	profile.SessionMode = sess.Mode
	plan, err := surface.CompileToolPlan(profile, host.RootCount)
	if err != nil {
		return prompts.Reader{}, err
	}
	if deps.Selection != nil {
		for name, ok := range deps.Selection.LoadedTools(sess.ID) {
			if ok && plan.Deferred(name) {
				plan = plan.Promote(name)
			}
		}
	}
	return prompts.NewReader(host, prompts.TurnProjection{
		ProfileID: prompts.CoordinatorProfileID,
		SurfaceID: profile.SurfaceID,
		Plan:      plan,
		Offered:   plan.ImmediateNames(),
	})
}

type fixtureToolLister struct {
	sess *api.Session
	list PromptToolLister
}

func (l fixtureToolLister) List(ctx context.Context, filter platform.ToolFilter) ([]tools.ToolMeta, error) {
	return l.list(ctx, l.sess, filter.ProfileID)
}
