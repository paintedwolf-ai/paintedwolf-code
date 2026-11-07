package prompts

import (
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/spawn"
)

// SkillSurfaceVarsInput carries the caller facts a pack skill body renders with.
type SkillSurfaceVarsInput struct {
	// ProfileID is the reader's tool profile id ("coordinator", "implement", ...).
	ProfileID string
	// SurfaceID is the coordinator turn surface; empty outside coordinator profiles.
	SurfaceID string
	// Offered names the tools on the reader's current model request.
	Offered []string
	// OnWorkerBranch reports a write-scoped worker on its own branch workspace.
	OnWorkerBranch bool
	// HasGit reports a usable git repository at the active workspace root.
	HasGit bool
	// RootCount is the number of workspace roots on the source.
	RootCount int
	// DelegationBudget is the spawn budget backing sizing hint vars.
	DelegationBudget spawn.WorkerToolBudget
}

// SkillSurfaceTemplateVars builds the template variables one skill read renders
// with: spawn policy and sizing facts, caller shape (is_coordinator,
// on_worker_branch, has_git, root_count), and the caller's live tool surface —
// coordinator path scopes and policy for coordinator profiles, profile
// capability vars for every other reader.
func SkillSurfaceTemplateVars(in SkillSurfaceVarsInput) (map[string]any, error) {
	vars := spawn.PolicyTemplateVars(in.DelegationBudget)
	spawn.RefreshSizingHintVars(vars)
	coordinator := in.ProfileID == CoordinatorProfileID
	setCallerShapeVars(vars, in, coordinator)
	if coordinator && in.SurfaceID != "" {
		turn := SurfaceTurn{Loaded: toolNameSet(in.Offered)}
		if err := MergeCoordinatorSurfacePathVars(in.SurfaceID, nil, vars, turn); err != nil {
			return nil, err
		}
		setCallerShapeVars(vars, in, coordinator)
		return vars, nil
	}
	profiles, err := sandbox.LoadToolProfiles()
	if err != nil {
		return nil, err
	}
	surface, err := LoadAgentToolSurface(in.ProfileID, in.Offered, nil, SurfaceTurn{}, profiles)
	if err != nil {
		return nil, err
	}
	for k, v := range toolProfileCapabilityTemplateVars(surface) {
		vars[k] = v
	}
	setCallerShapeVars(vars, in, coordinator)
	return vars, nil
}

// setCallerShapeVars writes the caller facts surface merges must not clobber.
func setCallerShapeVars(into map[string]any, in SkillSurfaceVarsInput, coordinator bool) {
	into["is_coordinator"] = coordinator
	into["on_worker_branch"] = in.OnWorkerBranch
	into["has_git"] = in.HasGit
	into["root_count"] = in.RootCount
}
