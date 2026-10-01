package session

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/hostresources"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/skills"
)

// SetSkillsGate controls project skill directories.
// A nil gate excludes project skills.
func (m *Manager) SetSkillsGate(gate *settings.ProjectSurfaceGate) {
	if m == nil {
		return
	}
	m.skillsGate = gate
}

// EffectiveSkills returns the skills available to a project: the device-resolved
// catalog, plus the project's own for ids the device does not already provide.
func (m *Manager) EffectiveSkills(ctx context.Context, projectID string, roots []string) ([]skills.Skill, []extpacks.Diagnostic) {
	projectDir := ""
	if len(roots) > 0 {
		projectDir = roots[0]
	}
	snapshot := m.hostResourceSnapshot(ctx, hostresources.ProjectContext{ID: projectID, Dir: projectDir}, nil)
	return m.effectiveSkillsForSurfaces(ctx, projectID, roots, nil, snapshot)
}

func (m *Manager) effectiveSkillsForSurfaces(
	ctx context.Context,
	projectID string,
	roots []string,
	surfaces []hostresources.ExecutionSurface,
	snapshot hostresources.Snapshot,
) ([]skills.Skill, []extpacks.Diagnostic) {
	if m == nil {
		device, diags := extpacks.LoadEffectiveSkills(extpacks.Active())
		return device, diags
	}
	projectID = strings.TrimSpace(projectID)
	eff, err := m.Catalog().EffectiveCatalogForProject(ctx, projectID)
	if err != nil || eff == nil {
		eff = m.Catalog().DeviceCatalog(ctx)
	}
	device, diags := extpacks.LoadEffectiveSkills(eff)
	if m.skillsGate == nil || !m.skillsGate.Applies(ctx, projectID) {
		return applySkillHostResources(device, diags, snapshot, surfaces)
	}
	if len(roots) == 0 && m.projects != nil && projectID != "" {
		if p, err := m.projects.Get(ctx, projectID); err == nil {
			roots = project.RootPaths(p)
		}
	}
	projectSkills, notes := m.skillsCache.Discover(roots)
	loaded, combinedDiags := extpacks.CombineSkills(device, diags, projectSkills, notes)
	return applySkillHostResources(loaded, combinedDiags, snapshot, surfaces)
}
