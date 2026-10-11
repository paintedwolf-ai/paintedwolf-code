package workflow

import (
	"github.com/lycaon/lycaon/internal/blueprint"
)

// WireBlueprintDepsForTest links BlueprintCreate/BlueprintGet on a test RunManager.
func WireBlueprintDepsForTest(mgr *RunManager, projectDir string) *blueprint.Manager {
	if mgr == nil {
		return nil
	}
	blueprintStore := blueprint.NewFileStoreForTest(projectDir)
	blueprintMgr := blueprint.NewManager(blueprintStore)
	blueprintMgr.AfterRetarget = mgr.Blueprints.RebindBlueprintPath
	mgr.Blueprints.Creator = blueprint.WorkflowBlueprintCreator{Manager: blueprintMgr}
	mgr.Blueprints.Getter = blueprintMgr
	mgr.Presentation.BlueprintGetter = blueprintMgr
	mgr.Approvals.Getter = blueprintMgr
	return blueprintMgr
}
