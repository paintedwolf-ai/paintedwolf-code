package profiles

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hostresources"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/pkg/api"
)

// CompileMachine builds one turn's host-resource snapshot, gated skills, and prompt surface.
func (m *Service) CompileMachine(ctx context.Context, sess *api.Session, profileID string) inject.Machine {
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
		Surface:    m.PromptSurface(ctx, sess, loaded, snapshot, surfaces, m.MutationCapable(ctx, sess, profileID)),
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
	out.Posture = m.EffectiveApprovalPosture(ctx, sess)
	out.Fingerprint = promptSurfaceFingerprint(out)
	return out
}

// EffectiveApprovalPosture resolves the session's effective approval posture
// from device policy and any repository .paintedwolf/approvals.yaml overlays,
// notifying the posture observer when the effective posture changes.
func (m *Service) EffectiveApprovalPosture(ctx context.Context, sess *api.Session) gate.Posture {
	if m == nil || sess == nil {
		return gate.PostureStrict
	}
	posture := m.resolveApprovalPosture(sess.ProjectID, m.approvalOverlayDirs(ctx, sess))
	if m.postureObserver != nil && strings.TrimSpace(sess.ID) != "" {
		m.postureObserver(ctx, sess.ID, posture)
	}
	return posture
}

func (m *Service) resolveApprovalPosture(projectID string, projectDirs []string) gate.Posture {
	projectID = strings.TrimSpace(projectID)
	posture := gate.DefaultPosture
	if m.approvals != nil {
		posture = m.approvals.Posture()
	}
	for _, dir := range projectDirs {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		if m.approvals != nil {
			posture = gate.Stricter(posture, m.approvals.Get(llm.SettingsScopeProject, settings.ProjectRef{ID: projectID, Dir: dir}).Posture)
			continue
		}
		posture = gate.Stricter(posture, m.approvals.ProjectOverlay(dir).Config.Posture)
	}
	return posture
}

func (m *Service) approvalOverlayDirs(ctx context.Context, sess *api.Session) []string {
	if sess == nil {
		return nil
	}
	var dirs []string
	add := func(dir string) {
		dir = strings.TrimSpace(dir)
		if dir != "" && !slices.Contains(dirs, dir) {
			dirs = append(dirs, dir)
		}
	}
	if m.workspace != nil {
		for _, dir := range m.workspace.OverlayRoots(ctx, sess) {
			add(dir)
		}
		if active, err := m.workspace.ActivePath(ctx, sess); err == nil {
			add(active)
		}
	}
	if len(dirs) == 0 && m.projects != nil && strings.TrimSpace(sess.ProjectID) != "" {
		if p, err := m.projects.Get(ctx, strings.TrimSpace(sess.ProjectID)); err == nil && p != nil {
			if res, err := project.ResolveProjectOverlay(p, sess.WorkspaceRootID); err == nil {
				for _, dir := range res.Paths {
					add(dir)
				}
			}
		}
	}
	add(sess.WorkspacePath)
	return dirs
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

// promptSurfaceFingerprint keys the stable prompt on what it renders: whether
// skills are available and the host resources. Skill identifiers and cards
// stay out of the prompt.
func promptSurfaceFingerprint(surface prompts.AgentPromptSurface) string {
	payload, err := json.Marshal(struct {
		HasSkills            bool
		HostResources        []prompts.AgentHostResourceView
		HostResourcesOmitted int
	}{len(surface.Skills) > 0, surface.HostResources, surface.HostResourcesOmitted})
	if err != nil {
		return surface.Fingerprint
	}
	sum := sha256.Sum256(payload)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
