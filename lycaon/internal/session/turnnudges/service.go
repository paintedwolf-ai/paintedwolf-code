package turnnudges

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/coordinator/surfacecatalog"
	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/pkg/api"
)

type Surface interface{ PromptTurnSurfaceID(string) string }
type Spend interface {
	ClaimWarning(context.Context, *api.Session, float64) bool
}

// Profiles resolves session-constrained tool profiles and profile IDs for turn nudges.
type Profiles interface {
	ForSession(context.Context, *api.Session) []sandbox.ToolProfile
	PromptToolProfile(context.Context, *api.Session) (string, error)
	CommandExternalHidden() bool
}

// Workspace resolves attached workspace root paths for a session.
type Workspace interface {
	Paths(context.Context, *api.Session) ([]string, error)
}

type Service struct {
	ledger    *turnload.Ledger
	Spend     Spend
	surface   Surface
	profiles  Profiles
	workspace Workspace
	prompts   prompts.PromptTemplateEngine
}

func New(ledger *turnload.Ledger, spend Spend) *Service {
	return &Service{ledger: ledger, Spend: spend}
}
func (m *Service) SetLedger(ledger *turnload.Ledger)               { m.ledger = ledger }
func (m *Service) SetSurface(surface Surface)                      { m.surface = surface }
func (m *Service) SetProfiles(profiles Profiles)                   { m.profiles = profiles }
func (m *Service) SetWorkspace(workspace Workspace)                { m.workspace = workspace }
func (m *Service) SetPrompts(prompts prompts.PromptTemplateEngine) { m.prompts = prompts }

func (m *Service) renderKick(ctx context.Context, reader prompts.Reader, id string, data map[string]any) (string, error) {
	if m == nil || m.prompts == nil {
		return "", nil
	}
	return m.prompts.RenderKick(ctx, reader, strings.TrimSpace(id), data)
}

func (m *Service) sessionRootCount(ctx context.Context, sess *api.Session) int {
	if m != nil && m.workspace != nil && sess != nil {
		if paths, err := m.workspace.Paths(ctx, sess); err == nil {
			return len(paths)
		}
	}
	if sess != nil && strings.TrimSpace(sess.WorkspacePath) != "" {
		return 1
	}
	if m != nil && m.workspace != nil {
		return 0
	}
	return 1
}

func (m *Service) sessionProfiles(ctx context.Context, sess *api.Session) []sandbox.ToolProfile {
	if m != nil && m.profiles != nil {
		if list := m.profiles.ForSession(ctx, sess); len(list) > 0 {
			return list
		}
	}
	list, _ := sandbox.LoadToolProfiles()
	return list
}

func (m *Service) resolveProfileID(ctx context.Context, sess *api.Session, explicit string) string {
	if id := strings.TrimSpace(explicit); id != "" {
		if list := m.sessionProfiles(ctx, sess); hasProfile(list, id) {
			return id
		}
		if resolved, err := prompts.ToolProfileForAgent(id); err == nil {
			return resolved
		}
	}
	if m != nil && m.profiles != nil && sess != nil {
		if id, err := m.profiles.PromptToolProfile(ctx, sess); err == nil && strings.TrimSpace(id) != "" {
			return strings.TrimSpace(id)
		}
	}
	if sess != nil && strings.TrimSpace(sess.AgentType) != "" {
		if resolved, err := prompts.ToolProfileForAgent(sess.AgentType); err == nil {
			return resolved
		}
	}
	return prompts.CoordinatorProfileID
}

func hasProfile(profiles []sandbox.ToolProfile, id string) bool {
	for _, p := range profiles {
		if p.ID == id {
			return true
		}
	}
	return false
}

// activeTurnReader constructs the Reader for the session's current tool-bearing turn.
func (m *Service) activeTurnReader(ctx context.Context, sess *api.Session, explicitProfileID string) prompts.Reader {
	if m == nil || sess == nil {
		return prompts.Reader{}
	}
	profiles := m.sessionProfiles(ctx, sess)
	if len(profiles) == 0 {
		return prompts.Reader{}
	}
	profileID := m.resolveProfileID(ctx, sess, explicitProfileID)
	rootCount := m.sessionRootCount(ctx, sess)
	sessionMode := string(api.SessionModeAgent)
	if sess.Mode == api.SessionModeAsk {
		sessionMode = string(api.SessionModeAsk)
	}
	var loaded map[string]bool
	if m.ledger != nil {
		loaded = m.ledger.Active(sess.ID)
	}
	externalHidden := m.profiles != nil && m.profiles.CommandExternalHidden()
	if !sess.IsWorkerChild() && profileID == prompts.CoordinatorProfileID {
		surfaceID := ""
		if m.surface != nil {
			surfaceID = strings.TrimSpace(m.surface.PromptTurnSurfaceID(sess.ID))
		}
		if surfaceID != "" {
			plan, err := surface.CompileToolPlan(surface.TurnProfile{SurfaceID: surfaceID, SessionMode: sess.Mode}, rootCount)
			if err == nil {
				r, rErr := prompts.NewReader(prompts.ReaderInput{
					ProfileID:             prompts.CoordinatorProfileID,
					SurfaceID:             surfaceID,
					Mode:                  surface.ExecutionModeFamily(surfaceID),
					SessionMode:           sessionMode,
					Profiles:              profiles,
					Plan:                  plan,
					Loaded:                loaded,
					RootCount:             rootCount,
					HasGit:                rootCount > 0,
					HasAnyGitRoot:         rootCount > 0,
					WebSearch:             true,
					CommandExternalHidden: externalHidden,
				})
				if rErr == nil {
					return r
				}
			}
		}
	}
	r, err := prompts.NewReader(prompts.ReaderInput{
		ProfileID:             profileID,
		SessionMode:           sessionMode,
		Profiles:              profiles,
		Loaded:                loaded,
		RootCount:             rootCount,
		HasGit:                rootCount > 0,
		HasAnyGitRoot:         rootCount > 0,
		WebSearch:             true,
		CommandExternalHidden: externalHidden,
	})
	if err != nil {
		return prompts.Reader{}
	}
	return r
}

// commandMutationCapable reports whether the session/leg can emit mutating
// command redirects on tool-bearing turns in its current mode, posture, and roots.
func (m *Service) commandMutationCapable(ctx context.Context, sess *api.Session, explicitProfileID string) bool {
	if m == nil || sess == nil || sess.Mode == api.SessionModeAsk || m.sessionRootCount(ctx, sess) == 0 {
		return false
	}
	profiles := m.sessionProfiles(ctx, sess)
	profileID := m.resolveProfileID(ctx, sess, explicitProfileID)
	mutationEffect, err := toolcontract.NewCommandEffects(toolcontract.CommandMutation)
	if err != nil {
		return false
	}
	surfaceID := ""
	if !sess.IsWorkerChild() && profileID == prompts.CoordinatorProfileID && m.surface != nil {
		surfaceID = strings.TrimSpace(m.surface.PromptTurnSurfaceID(sess.ID))
	}
	effects, err := surfacecatalog.AdmittedCommandEffects(profiles, profileID, surfaceID)
	if err != nil || !effects.Allows(mutationEffect) {
		return false
	}
	for _, p := range profiles {
		if p.ID != profileID {
			continue
		}
		if !p.ToolAllowed("command") {
			return false
		}
		if surfaceID != "" {
			if plan, pErr := surface.CompileToolPlan(surface.TurnProfile{SurfaceID: surfaceID, SessionMode: sess.Mode}, m.sessionRootCount(ctx, sess)); pErr == nil {
				return plan.Addressable("command")
			}
		}
		return true
	}
	return false
}
