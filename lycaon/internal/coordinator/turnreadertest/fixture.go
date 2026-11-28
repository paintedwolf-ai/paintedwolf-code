// Package turnreadertest projects fixture requests for assembly tests that run
// without a prompt loop: through the turn pipeline over the fixture's tool
// lister, or from the declared surface and profile tools when it lists none.
package turnreadertest

import (
	"context"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/catalogview"
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

// Fixture is the part of an assembly fixture a request projection reads.
type Fixture struct {
	Lister         func(ctx context.Context, sess *api.Session, profileID string) ([]tools.ToolMeta, error)
	View           func(ctx context.Context, sess *api.Session) *catalogview.View
	Loaded         func(sessionID string) map[string]bool
	WebSearch      func() bool
	Roots          func(ctx context.Context, sess *api.Session) ([]map[string]any, int, string, int)
	ImplementState func(ctx context.Context, sess *api.Session) surface.ImplementSessionState
	CommandJobs    func(sessionID string) bool
	HeldCalls      func(sessionID string) bool
}

// TurnReader derives the Reader of the fixture request frame projects.
func (f Fixture) TurnReader(ctx context.Context, sess *api.Session, history []api.Message, frame inject.CoordinatorTurnFrame) (prompts.Reader, error) {
	host, err := f.hostFacts(ctx, sess)
	if err != nil {
		return prompts.Reader{}, err
	}
	if f.Lister == nil {
		return f.declaredReader(ctx, host, sess, history, frame)
	}
	profileID := strings.TrimSpace(frame.Machine.ProfileID)
	if profileID == "" && sess.IsWorkerChild() {
		return prompts.Reader{}, prompts.ErrUnknownAgent
	}
	if profileID != "" && !slices.ContainsFunc(host.Profiles, func(p sandbox.ToolProfile) bool { return p.ID == profileID }) {
		// A fixture profile outside the shipped catalog holds what its lister lists.
		metas, err := f.Lister(ctx, sess, profileID)
		if err != nil {
			return prompts.Reader{}, err
		}
		profile := sandbox.ToolProfile{ID: profileID, Tools: map[string]bool{}}
		for _, meta := range metas {
			profile.Tools[meta.Name] = true
		}
		host.Profiles = append(slices.Clone(host.Profiles), profile)
	}
	loop := promptloop.ContextDeps{
		Policy: toolpolicy.NewEngine(toolpolicy.EngineDeps{
			ToolLister:       lister{sess: sess, list: f.Lister},
			ProjectRootCount: func(context.Context, *api.Session) int { return host.RootCount },
		}),
		ImplementSessionState: f.ImplementState,
		LoadedTools:           f.Loaded,
		ProjectRootCount:      func(context.Context, *api.Session) int { return host.RootCount },
		HostFacts:             func(context.Context, *api.Session) (prompts.HostFacts, error) { return host, nil },
		LiveResources: func(sessionID string) toolcontract.ResourcePresence {
			var live toolcontract.ResourcePresence
			if f.CommandJobs != nil {
				live.CommandJobs = f.CommandJobs(sessionID)
			}
			if f.HeldCalls != nil {
				live.HeldCalls = f.HeldCalls(sessionID)
			}
			return live
		},
		ContextSurfaceDeps: promptloop.ContextSurfaceDeps{WebSearchEnabled: func() bool { return host.WebSearch }},
	}
	return promptloop.TurnReader(ctx, loop, sess, history, frame)
}

func (f Fixture) hostFacts(ctx context.Context, sess *api.Session) (prompts.HostFacts, error) {
	profiles, err := sandbox.LoadToolProfiles()
	if err != nil {
		return prompts.HostFacts{}, err
	}
	host := prompts.HostFacts{Profiles: profiles, WebSearch: true}
	if f.View != nil {
		if view := f.View(ctx, sess); view != nil {
			if len(view.ToolProfiles) > 0 {
				host.Profiles = view.ToolProfiles
			}
			host.Schemas = view.ToolSchemas
		}
	}
	if sess.Mode == api.SessionModeAsk {
		host.SessionMode = string(api.SessionModeAsk)
	}
	if f.WebSearch != nil {
		host.WebSearch = f.WebSearch()
	}
	if f.Roots != nil {
		_, host.RootCount, _, _ = f.Roots(ctx, sess)
	} else if strings.TrimSpace(sess.WorkspacePath) != "" {
		host.RootCount = 1
	}
	if !sess.IsWorkerChild() {
		host.SpawnAllowlist = spawn.AmbientAllowedAgents()
	}
	return host, nil
}

func (f Fixture) declaredReader(ctx context.Context, host prompts.HostFacts, sess *api.Session, history []api.Message, frame inject.CoordinatorTurnFrame) (prompts.Reader, error) {
	if sess.IsWorkerChild() {
		return prompts.NewReader(host, prompts.TurnProjection{ProfileID: frame.Machine.ProfileID, Declared: true})
	}
	var implState surface.ImplementSessionState
	if f.ImplementState != nil {
		implState = f.ImplementState(ctx, sess)
	}
	profile := surface.ResolveTurnProfile(frame.RunContext, sess, history, implState)
	profile.SessionMode = sess.Mode
	plan, err := surface.CompileToolPlan(profile, host.RootCount)
	if err != nil {
		return prompts.Reader{}, err
	}
	if f.Loaded != nil {
		for name, ok := range f.Loaded(sess.ID) {
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

type lister struct {
	sess *api.Session
	list func(ctx context.Context, sess *api.Session, profileID string) ([]tools.ToolMeta, error)
}

func (l lister) List(ctx context.Context, filter platform.ToolFilter) ([]tools.ToolMeta, error) {
	return l.list(ctx, l.sess, filter.ProfileID)
}
