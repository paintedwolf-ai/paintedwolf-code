package session

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/agentdef"
	"github.com/lycaon/lycaon/internal/hostresources"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/pkg/api"
)

func (m *Manager) skillSelectorForSession(ctx context.Context, sess *api.Session) skills.Selector {
	profile, ok := m.agentProfileForSession(ctx, sess)
	if !ok {
		return skills.Selector{}
	}
	return profile.Skills
}

func (m *Manager) agentProfileForSession(ctx context.Context, sess *api.Session) (agentdef.Profile, bool) {
	if m == nil || sess == nil {
		return agentdef.Profile{}, false
	}
	agentID := strings.TrimSpace(sess.AgentType)
	if agentID == "" {
		agentID = orchestration.ProfileCoordinator
	}
	resolver := m.agents
	if view := m.Catalog().ViewForSession(ctx, sess); view != nil {
		resolver = view
	}
	if resolver == nil {
		return agentdef.Profile{}, false
	}
	profile, err := resolver.Get(agentID)
	if err != nil {
		return agentdef.Profile{}, false
	}
	return profile, true
}

func (m *Manager) executionSurfacesForProfile(
	ctx context.Context,
	sess *api.Session,
	profileID string,
) []hostresources.ExecutionSurface {
	profile, ok := m.toolProfileByID(ctx, sess, profileID)
	if !ok {
		return nil
	}
	return executionSurfacesForToolProfile(profile)
}

func executionSurfacesForToolProfile(profile sandbox.ToolProfile) []hostresources.ExecutionSurface {
	surfaces := []hostresources.ExecutionSurface{}
	if profile.ToolAllowed("command") || profile.ToolAllowed("terminal_open") {
		surfaces = append(surfaces, hostresources.SurfaceProcessExec)
	}
	return surfaces
}

func (m *Manager) profileMutationCapable(ctx context.Context, sess *api.Session, profileID string) bool {
	profile, ok := m.toolProfileByID(ctx, sess, profileID)
	if !ok {
		return false
	}
	return prompts.ToolProfileMutationCapable(profile)
}

func (m *Manager) toolProfileByID(ctx context.Context, sess *api.Session, profileID string) (sandbox.ToolProfile, bool) {
	profileID = strings.TrimSpace(profileID)
	if m == nil || profileID == "" {
		return sandbox.ToolProfile{}, false
	}
	view := m.Catalog().ViewForSession(ctx, sess)
	if view == nil {
		return sandbox.ToolProfile{}, false
	}
	for _, profile := range view.ToolProfiles {
		if profile.ID == profileID {
			return profile, true
		}
	}
	return sandbox.ToolProfile{}, false
}
