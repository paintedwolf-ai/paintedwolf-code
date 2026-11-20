package workflowtest

import (
	"github.com/lycaon/lycaon/internal/blueprint"
	"github.com/lycaon/lycaon/internal/blueprint/blueprinttest"
	"github.com/lycaon/lycaon/internal/workflow"
)

// WireBlueprintDeps links BlueprintCreate/BlueprintGet on a test RunManager.
func WireBlueprintDeps(mgr *workflow.RunManager, projectDir string) *blueprint.Manager {
	if mgr == nil {
		return nil
	}
	blueprintStore := blueprinttest.NewFileStore(projectDir)
	blueprintMgr := blueprint.NewManager(blueprintStore)
	blueprintMgr.AfterRetarget = mgr.Blueprints.RebindBlueprintPath
	mgr.Blueprints.Creator = blueprint.WorkflowBlueprintCreator{Manager: blueprintMgr}
	mgr.Blueprints.Getter = blueprintMgr
	mgr.Presentation.BlueprintGetter = blueprintMgr
	mgr.Approvals.Getter = blueprintMgr
	return blueprintMgr
}
