package session

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/hostresources"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/pkg/api"
)

// CompileMachine builds one turn's host-resource snapshot, gated skills, and prompt surface.
func (m *Manager) CompileMachine(ctx context.Context, sess *api.Session, profileID string) inject.Machine {
	if m == nil || sess == nil {
		return inject.Machine{}
	}
	profileID = strings.TrimSpace(profileID)
	roots, err := m.sessionRootPaths(ctx, sess)
	if err != nil {
		return inject.Machine{ProfileID: profileID}
	}
	projectDir, _ := m.sessionActiveRootPath(ctx, sess)
	project := hostresources.ProjectContext{ID: sess.ProjectID, Dir: projectDir}
	snapshot := m.hostResourceSnapshot(ctx, project, m.overlayRootPaths(ctx, sess))
	surfaces := m.executionSurfacesForProfile(ctx, sess, profileID)
	loaded, _ := m.effectiveSkillsForSurfaces(ctx, sess.ProjectID, roots, surfaces, snapshot)
	loaded = m.skillSelectorForSession(ctx, sess).Select(loaded)
	return inject.Machine{
		ProfileID:  profileID,
		SkillCount: len(loaded),
		Surface:    m.promptSurfaceFrom(ctx, sess, loaded, snapshot, surfaces, m.profileMutationCapable(ctx, sess, profileID)),
		ReadRoots:  skillReadRoots(loaded),
	}
}

func (m *Manager) hostResourceSnapshot(
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
func (m *Manager) EffectiveSkillsForProfile(
	ctx context.Context,
	sess *api.Session,
	profileID string,
	roots []string,
) ([]skills.Skill, []extpacks.Diagnostic) {
	projectID := ""
	projectDir := ""
	if sess != nil {
		projectID = sess.ProjectID
		projectDir, _ = m.sessionActiveRootPath(ctx, sess)
	}
	if projectDir == "" && len(roots) > 0 {
		projectDir = roots[0]
	}
	project := hostresources.ProjectContext{ID: projectID, Dir: projectDir}
	var overlayRoots []string
	if sess != nil {
		overlayRoots = m.overlayRootPaths(ctx, sess)
	}
	snapshot := m.hostResourceSnapshot(ctx, project, overlayRoots)
	surfaces := m.executionSurfacesForProfile(ctx, sess, profileID)
	loaded, diags := m.effectiveSkillsForSurfaces(ctx, projectID, roots, surfaces, snapshot)
	return m.skillSelectorForSession(ctx, sess).Select(loaded), diags
}

func skillReadRoots(loaded []skills.Skill) []string {
	dirs := make([]string, 0, len(loaded))
	for _, sk := range loaded {
		if dir := strings.TrimSpace(sk.Dir); dir != "" {
			dirs = append(dirs, dir)
		}
	}
	return dirs
}

func (m *Manager) promptSurfaceFrom(
	ctx context.Context,
	sess *api.Session,
	loaded []skills.Skill,
	snapshot hostresources.Snapshot,
	surfaces []hostresources.ExecutionSurface,
	mutationCapable bool,
) prompts.AgentPromptSurface {
	out := prompts.AgentPromptSurface{}
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
	out.Fingerprint = promptSurfaceFingerprint(out)
	return out
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
