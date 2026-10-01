package workflow

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/blueprint"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

// BlueprintSeeder creates and reloads draft blueprint files for launch seeding.
type BlueprintSeeder interface {
	Create(ctx context.Context, projectID, title, path, sourceWorkflowID, content string) (*api.Blueprint, error)
	Get(ctx context.Context, projectID, path string) (*api.Blueprint, error)
}

// LaunchSourceCompatible checks the blueprint path convention.
func LaunchSourceCompatible(sourcePath string) bool {
	return blueprint.ValidateConventionPath(filepath.ToSlash(strings.TrimSpace(sourcePath))) == nil
}

// CompatibleWorkflowIDs returns blueprint-supporting workflow ids for a convention source.
// Plan is sorted first among matches. One entry per workflow id.
func CompatibleWorkflowIDs(sourcePath string, manifests []workflowdef.Manifest) []string {
	sourcePath = strings.TrimSpace(sourcePath)
	if sourcePath == "" || !LaunchSourceCompatible(sourcePath) {
		return nil
	}
	seen := map[string]struct{}{}
	var ids []string
	for _, m := range manifests {
		if !workflowdef.SupportsBlueprints(m) {
			continue
		}
		if m.Blueprint != nil {
			if p := strings.TrimSpace(m.Blueprint.Path); p != "" {
				if err := blueprint.ValidateConventionPath(p); err != nil {
					continue
				}
			}
		}
		id := strings.TrimSpace(m.ID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	sort.SliceStable(ids, func(i, j int) bool {
		if ids[i] == "plan" {
			return true
		}
		if ids[j] == "plan" {
			return false
		}
		return ids[i] < ids[j]
	})
	return ids
}

// ResolveLaunchTarget picks the manifest for a blueprint launch. An empty
// targetWorkflowID takes the first compatible entry.
func ResolveLaunchTarget(sourcePath, targetWorkflowID string, manifests []workflowdef.Manifest) (workflowdef.Manifest, error) {
	sourcePath = strings.TrimSpace(sourcePath)
	targetWorkflowID = strings.TrimSpace(targetWorkflowID)
	compat := CompatibleWorkflowIDs(sourcePath, manifests)
	if len(compat) == 0 {
		return workflowdef.Manifest{}, ErrBlueprintLaunchIncompatible
	}
	if targetWorkflowID == "" {
		// CompatibleWorkflowIDs already orders the default first.
		targetWorkflowID = compat[0]
	}
	allowed := false
	for _, id := range compat {
		if id == targetWorkflowID {
			allowed = true
			break
		}
	}
	if !allowed {
		return workflowdef.Manifest{}, ErrBlueprintLaunchIncompatible
	}
	for _, m := range manifests {
		if strings.TrimSpace(m.ID) == targetWorkflowID && workflowdef.SupportsBlueprints(m) {
			return m, nil
		}
	}
	return workflowdef.Manifest{}, ErrBlueprintLaunchUnsupported
}

// LaunchFromBlueprint seeds a draft and optionally starts its workflow.
func (m *RunManager) LaunchFromBlueprint(
	ctx context.Context,
	sessionID string,
	source *api.Blueprint,
	target workflowdef.Manifest,
	seeders BlueprintSeeder,
	projectDir string,
	deferStart bool,
) (*api.WorkflowRun, *api.Blueprint, error) {
	if m == nil || seeders == nil || source == nil {
		return nil, nil, fmt.Errorf("launch deps required")
	}
	if !workflowdef.SupportsBlueprints(target) {
		return nil, nil, ErrBlueprintLaunchUnsupported
	}
	if !LaunchSourceCompatible(source.Path) {
		return nil, nil, ErrBlueprintLaunchIncompatible
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, nil, fmt.Errorf("session_id required")
	}
	projectDir = strings.TrimSpace(projectDir)
	if projectDir == "" {
		return nil, nil, fmt.Errorf("project_dir required")
	}

	title := strings.TrimSpace(source.Title)
	if title == "" {
		title = strings.TrimSpace(target.ID)
	}
	if title == "" || strings.EqualFold(title, "plan") {
		title = "blueprint"
	}
	content := blueprint.ResetToDraftFrontmatter(source.Content)
	seed, err := seeders.Create(ctx, source.ProjectID, title, "", target.ID, content)
	if err != nil {
		return nil, nil, err
	}

	// Materialize into the session workspace when it differs from the blueprint store root.
	if err := WriteBlueprintFile(projectDir, seed.Path, seed.Content); err != nil {
		return nil, nil, fmt.Errorf("materialize blueprint file: %w", err)
	}

	seed, err = seeders.Get(ctx, source.ProjectID, seed.Path)
	if err != nil {
		return nil, seed, err
	}

	if deferStart {
		if err := m.SetPendingBlueprintLaunchPath(ctx, sessionID, seed.Path); err != nil {
			return nil, seed, err
		}
		return nil, seed, nil
	}

	run, err := m.StartHuman(ctx, sessionID, api.StartWorkflowRunRequest{
		WorkflowID:      target.ID,
		WorkflowVersion: target.Version,
		BlueprintPath:   seed.Path,
		BlueprintTitle:  title,
	})
	if err != nil {
		return nil, nil, err
	}

	seed, err = seeders.Get(ctx, source.ProjectID, seed.Path)
	if err != nil {
		return run, seed, err
	}
	return run, seed, nil
}
