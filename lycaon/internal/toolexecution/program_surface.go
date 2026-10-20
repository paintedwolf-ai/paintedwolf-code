package toolexecution

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
)

// OfferProgramOperations freezes only explicitly selected, profile-visible
// operations. Registry presence and command discovery are not exposure grants.
// Each reached child still enters normal execution and effect admission.
func (e *Metadata) OfferProgramOperations(ctx context.Context, filter platform.ToolFilter, active []string) (commandprogram.OfferedOperations, []toolcontract.OperationDefinition, error) {
	if e == nil || e.registry == nil || filter.ProfileID == "" {
		return commandprogram.Offer(), nil, fmt.Errorf("program surface requires registry and explicit profile")
	}
	definitions := make([]toolcontract.OperationDefinition, 0, len(active))
	seen := make(map[string]bool, len(active))
	for _, identity := range active {
		if identity == "" || seen[identity] {
			return commandprogram.Offer(), nil, fmt.Errorf("program surface requires unique nonempty operation identities")
		}
		seen[identity] = true
		registered, ok := e.registry.Definition(identity)
		if !ok {
			return commandprogram.Offer(), nil, fmt.Errorf("program operation %q has no registered implementation", identity)
		}
		decision, err := tools.EvaluateListVisible(ctx, e.policy, platform.PolicyContext{
			ProfileID: filter.ProfileID, ToolAccess: filter.ToolAccess, ToolName: identity,
		})
		if err != nil {
			return commandprogram.Offer(), nil, err
		}
		if decision == nil || !decision.Allowed {
			continue
		}
		definitions = append(definitions, registered.Operation)
	}
	return commandprogram.Offer(definitions...), definitions, nil
}
