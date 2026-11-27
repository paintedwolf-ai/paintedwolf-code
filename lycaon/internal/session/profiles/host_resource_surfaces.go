package profiles

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/agentdef"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hostresources"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/pkg/api"
)

func (m *Service) SkillSelector(ctx context.Context, sess *api.Session) skills.Selector {
	profile, ok := m.AgentProfile(ctx, sess)
	if !ok {
		return skills.Selector{}
	}
	return profile.Skills
}

func (m *Service) AgentProfile(ctx context.Context, sess *api.Session) (agentdef.Profile, bool) {
	if m == nil || sess == nil {
		return agentdef.Profile{}, false
	}
	agentID := strings.TrimSpace(sess.AgentType)
	if agentID == "" {
		agentID = orchestration.ProfileCoordinator
	}
	resolver := m.Agents
	if view := m.catalog.ViewForSession(ctx, sess); view != nil {
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

// HostResourceReach is what the session's profile can do with host resources.
// Ask mode starts no processes, so it reaches none.
func (m *Service) HostResourceReach(
	ctx context.Context,
	sess *api.Session,
	profileID string,
) hostresources.Reach {
	if sess != nil && sess.Mode == api.SessionModeAsk {
		return hostresources.Reach{}
	}
	profile, ok := m.ToolProfile(ctx, sess, profileID)
	if !ok {
		return hostresources.Reach{}
	}
	role := hostresources.RoleCoordinator
	if sess != nil && sess.IsWorkerChild() {
		role = hostresources.RoleWorker
	}
	return ReachForToolProfile(profile, role, !confine.Available())
}

// ReachForToolProfile derives a profile's host-resource reach from the process
// tools it allows.
func ReachForToolProfile(profile sandbox.ToolProfile, role hostresources.AgentRole, unconfined bool) hostresources.Reach {
	tools := processTools(profile)
	reach := hostresources.Reach{Role: role, Unconfined: unconfined}
	if len(tools) == 0 {
		return reach
	}
	reach.Surfaces = []hostresources.ExecutionSurface{hostresources.SurfaceProcessExec}
	reach.Offered = hostresources.OfferedCapabilities(tools)
	return reach
}

// processTools are the profile's tools that start host processes.
func processTools(profile sandbox.ToolProfile) []string {
	extExec, _ := toolcontract.NewCommandEffects(toolcontract.CommandExternalExec)
	var tools []string
	if profile.ToolAllowed("command") && (profile.CommandEffects == 0 || profile.CommandEffects.Allows(extExec)) {
		tools = append(tools, "command")
	}
	if profile.ToolAllowed("terminal_open") {
		tools = append(tools, "terminal_open")
	}
	return tools
}

func (m *Service) MutationCapable(ctx context.Context, sess *api.Session, profileID string) bool {
	if sess != nil && sess.Mode == api.SessionModeAsk {
		return false
	}
	profile, ok := m.ToolProfile(ctx, sess, profileID)
	if !ok {
		return false
	}
	return prompts.ToolProfileMutationCapable(profile)
}

func (m *Service) ToolProfile(ctx context.Context, sess *api.Session, profileID string) (sandbox.ToolProfile, bool) {
	profileID = strings.TrimSpace(profileID)
	if m == nil || profileID == "" {
		return sandbox.ToolProfile{}, false
	}
	view := m.catalog.ViewForSession(ctx, sess)
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
