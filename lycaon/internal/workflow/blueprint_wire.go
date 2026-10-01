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
	blueprintMgr.AfterRetarget = mgr.RebindBlueprintPath
	mgr.BlueprintCreate = blueprint.WorkflowBlueprintCreator{Manager: blueprintMgr}
	mgr.BlueprintGet = blueprintMgr
	return blueprintMgr
}
