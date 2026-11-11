package profiles

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hostresources"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/pkg/api"
)

// CompileMachine builds one turn's host-resource snapshot, gated skills, and prompt surface.
func (m *Service) CompileMachine(ctx context.Context, sess *api.Session, profileID string) inject.Machine {
	return m.compileMachine(ctx, sess, profileID, true)
}

// ProjectMachine reads the prompt surface without delivering posture changes.
func (m *Service) ProjectMachine(ctx context.Context, sess *api.Session, profileID string) inject.Machine {
	return m.compileMachine(ctx, sess, profileID, false)
}

func (m *Service) compileMachine(ctx context.Context, sess *api.Session, profileID string, observe bool) inject.Machine {
	if m == nil || sess == nil {
		return inject.Machine{}
	}
	profileID = strings.TrimSpace(profileID)
	roots, err := m.workspace.Paths(ctx, sess)
	if err != nil {
		return inject.Machine{ProfileID: profileID}
	}
	projectDir, _ := m.workspace.ActivePath(ctx, sess)
	project := hostresources.ProjectContext{ID: sess.ProjectID, Dir: projectDir}
	snapshot := m.hostResourceSnapshot(ctx, project, m.workspace.SettingsRoots(ctx, sess))
	surfaces := m.ExecutionSurfaces(ctx, sess, profileID)
	loaded, _ := m.effectiveSkillsForSurfaces(ctx, sess.ProjectID, roots, surfaces, snapshot)
	loaded = m.SkillSelector(ctx, sess).Select(loaded)
	return inject.Machine{
		ProfileID:  profileID,
		SkillCount: len(loaded),
		Surface:    m.promptSurface(ctx, sess, loaded, snapshot, surfaces, m.MutationCapable(ctx, sess, profileID), observe),
		ReadRoots:  SkillReadRoots(loaded),
	}
}

func (m *Service) hostResourceSnapshot(
	ctx context.Context,
	project hostresources.ProjectContext,
	overlayRoots []string,
) hostresources.Snapshot {
	if m == nil || m.hostResources == nil {
		return hostresources.Snapshot{}
	}
	return m.hostResources.SnapshotFor(ctx, false, project, overlayRoots)
}

// EffectiveSkillsForProfile intersects host-resource-gated skills with the
// turn profile's execution surfaces.
func (m *Service) EffectiveSkillsForProfile(
	ctx context.Context,
	sess *api.Session,
	profileID string,
	roots []string,
) ([]skills.Skill, []extpacks.Diagnostic) {
	projectID := ""
	projectDir := ""
	if sess != nil {
		projectID = sess.ProjectID
		projectDir, _ = m.workspace.ActivePath(ctx, sess)
	}
	if projectDir == "" && len(roots) > 0 {
		projectDir = roots[0]
	}
	project := hostresources.ProjectContext{ID: projectID, Dir: projectDir}
	var overlayRoots []string
	if sess != nil {
		overlayRoots = m.workspace.SettingsRoots(ctx, sess)
	}
	snapshot := m.hostResourceSnapshot(ctx, project, overlayRoots)
	surfaces := m.ExecutionSurfaces(ctx, sess, profileID)
	loaded, diags := m.effectiveSkillsForSurfaces(ctx, projectID, roots, surfaces, snapshot)
	return m.SkillSelector(ctx, sess).Select(loaded), diags
}

func SkillReadRoots(loaded []skills.Skill) []string {
	dirs := make([]string, 0, len(loaded))
	for _, sk := range loaded {
		if dir := strings.TrimSpace(sk.Dir); dir != "" {
			dirs = append(dirs, dir)
		}
	}
	return dirs
}

func (m *Service) PromptSurface(
	ctx context.Context,
	sess *api.Session,
	loaded []skills.Skill,
	snapshot hostresources.Snapshot,
	surfaces []hostresources.ExecutionSurface,
	mutationCapable bool,
) prompts.AgentPromptSurface {
	return m.promptSurface(ctx, sess, loaded, snapshot, surfaces, mutationCapable, true)
}

func (m *Service) promptSurface(ctx context.Context, sess *api.Session, loaded []skills.Skill, snapshot hostresources.Snapshot, surfaces []hostresources.ExecutionSurface, mutationCapable, observe bool) prompts.AgentPromptSurface {
	out := prompts.AgentPromptSurface{Posture: gate.PostureStrict}
	if m == nil || sess == nil {
		return out
	}
	out.Skills = agentSkillViews(loaded)
	ambient := hostresources.ProjectAmbient(hostresources.AmbientInput{
		Snapshot:        snapshot,
		Surfaces:        surfaces,
		MutationCapable: mutationCapable,
	})
	out.HostResources = hostResourceViews(ambient)
	out.HostResourcesOmitted = ambient.Omitted
	out.Posture = m.effectiveApprovalPosture(ctx, sess, observe)
	if m.commandExternalHidden != nil {
		out.CommandExternalHidden = m.commandExternalHidden()
		if observe && m.externalObserver != nil && strings.TrimSpace(sess.ID) != "" {
			m.externalObserver(ctx, sess.ID, out.Posture, out.CommandExternalHidden)
		}
	}
	out.Fingerprint = out.CacheFingerprint()
	return out
}

// EffectiveApprovalPosture resolves the session's effective approval posture
// through settings.ApprovalStore.EffectivePosture, notifying the posture
// observer when the effective posture changes.
func (m *Service) EffectiveApprovalPosture(ctx context.Context, sess *api.Session) gate.Posture {
	return m.effectiveApprovalPosture(ctx, sess, true)
}

func (m *Service) effectiveApprovalPosture(ctx context.Context, sess *api.Session, observe bool) gate.Posture {
	if m == nil || sess == nil {
		return gate.PostureStrict
	}
	var projectDir string
	if m.workspace != nil {
		projectDir, _ = m.workspace.ActivePath(ctx, sess)
	}
	if strings.TrimSpace(projectDir) == "" {
		projectDir = sess.WorkspacePath
	}
	posture := m.approvals.EffectivePosture(settings.ProjectRef{ID: sess.ProjectID, Dir: projectDir})
	if observe && m.postureObserver != nil && strings.TrimSpace(sess.ID) != "" {
		m.postureObserver(ctx, sess.ID, posture)
	}
	return posture
}

func agentSkillViews(loaded []skills.Skill) []prompts.AgentSkillView {
	out := make([]prompts.AgentSkillView, 0, len(loaded))
	for _, sk := range loaded {
		out = append(out, prompts.AgentSkillView{Name: sk.Name, Description: strings.TrimSpace(sk.Description)})
	}
	return out
}

func hostResourceViews(plan hostresources.AmbientPlan) []prompts.AgentHostResourceView {
	out := make([]prompts.AgentHostResourceView, 0, len(plan.Resources))
	for _, row := range plan.Resources {
		out = append(out, prompts.AgentHostResourceView{
			ID: row.ID, Label: row.Label, Category: row.Category,
			Status: row.Status, Access: row.Access, Guidance: row.Guidance,
		})
	}
	return out
}
