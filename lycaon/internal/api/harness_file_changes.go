package api

import (
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func applyHarnessFileChanges(request *hitl.CheckpointRequest, changes []wire.ApprovalFileChange) {
	if len(changes) == 0 {
		return
	}
	action := request.ProposedAction
	action.Presentation.Command = ""
	action.Mutations.FileChanges = changes
	for _, change := range changes {
		action.Invocation.Files = append(action.Invocation.Files, change.Path)
		for _, path := range []string{change.Path, change.FromPath} {
			if change.Target == "index" {
				continue
			}
			if target, ok := hitl.AgentPolicyTargetFor(path, action.Scope.ProjectDir); ok {
				action.Mutations.AgentPolicy = append(action.Mutations.AgentPolicy, target)
			}
		}
	}
	_, request.Decision = gate.Evaluate(gate.Facts{
		Stage: gate.StagePreSpawn, Ran: gate.ProducerApprovalRequest | gate.ProducerFilePath,
		ApprovalRequest: &gate.ApprovalRequest{Count: 1}, AgentPolicyPaths: hitl.AgentPolicyPaths(action.Mutations.AgentPolicy),
	}, gate.DefaultPosture)
}
