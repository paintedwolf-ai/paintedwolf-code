package delegation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// RegisterDispatchTool registers the coordinator delegate_dispatch tool on the registry.
func RegisterDispatchTool(reg *tools.DefaultRegistry, mgr *Manager) error {
	if reg == nil || mgr == nil {
		return fmt.Errorf("registry and manager required")
	}
	return reg.Register("delegate_dispatch", func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		delegationID, ok := mgr.Store.DelegationBySessionID(tctx.SessionID)
		if !ok {
			return "", &tools.ToolReject{
				Code: "COORDINATOR_DELEGATE_DISPATCH_USE_TASK",
			}
		}
		legID, _ := args["leg_id"].(string)
		legID = strings.TrimSpace(legID)
		if legID == "" {
			legs, err := mgr.Store.ListLegs(ctx, delegationID)
			if err != nil || len(legs) == 0 {
				return "", &tools.ToolReject{
					Code: "DELEGATE_DISPATCH_LEG_REQUIRED",
					Data: map[string]any{"delegation_id": delegationID},
				}
			}
			for _, leg := range legs {
				if leg.Status == api.LegStatusPending {
					legID = leg.ID
					break
				}
			}
			if legID == "" {
				return "", &tools.ToolReject{
					Code: "DELEGATE_DISPATCH_LEG_NOT_PENDING",
					Data: map[string]any{"delegation_id": delegationID},
				}
			}
		}
		leg, err := mgr.DispatchLeg(ctx, delegationID, legID, tctx.ToolCallID)
		if errors.Is(err, ErrLegNotPending) {
			return "", &tools.ToolReject{
				Code: "DELEGATE_DISPATCH_LEG_NOT_PENDING",
				Data: map[string]any{"delegation_id": delegationID, "leg_id": legID},
			}
		}
		if err != nil {
			return "", err
		}
		if tctx.Out != nil {
			tctx.Out.OwnerRef = leg.WorkerID
			tctx.Out.Dispatch = &api.WorkerDispatch{
				WorkerID: leg.WorkerID, AgentType: leg.AgentType, DelegationID: delegationID,
				LegID: leg.ID,
			}
		}
		out := map[string]any{
			"delegation_id": delegationID,
			"leg_id":        leg.ID,
			"worker_id":     leg.WorkerID,
			"status":        "enqueued",
			"next_step":     "Wait for worker_summary on this session before asserting completion.",
		}
		raw, _ := json.Marshal(out)
		return string(raw), nil
	})
}
