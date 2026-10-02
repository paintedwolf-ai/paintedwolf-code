package tools

import (
	"context"

	"github.com/lycaon/lycaon/internal/approvaloutcome"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// SetLocalListenGate wires pre-spawn listener review.
func (e *DefaultToolExecutor) SetLocalListenGate(gate LocalListenGate) {
	if e != nil {
		e.localListenGate = gate
	}
}

// preflightLocalListenCapability resolves a listener field before spawn.
func (e *DefaultToolExecutor) preflightLocalListenCapability(
	ctx context.Context,
	tool string,
	args map[string]any,
	tc ToolContext,
) (*LocalListenResult, error) {
	if !tc.Invocation.Contract.Supports(toolcontract.CapabilityLocalListen) {
		return nil, nil
	}
	capReq, reject := ParseCapabilityRequest(args)
	if reject != nil {
		return nil, e.rejectBeforeInvoke(ctx, tool, tc.Agent, args, reject)
	}
	if capReq == nil || capReq.LocalListen == nil {
		return nil, nil
	}
	ports := append([]uint16(nil), capReq.LocalListen.Ports...)
	// Direct IP includes listener authority.
	if tc.DirectIPRequested {
		return &LocalListenResult{Authorized: true, Ports: nil}, nil
	}
	if tc.LocalListenGranted && axisPortsCovered(tc.LocalListenPorts, ports) {
		return &LocalListenResult{Authorized: true, Ports: spawnPortNarrowing(tc.LocalListenPorts, ports)}, nil
	}
	if e.localListenGate == nil {
		return nil, e.rejectBeforeInvoke(ctx, tool, tc.Agent, args, &ToolReject{
			Code: isolation.CodeApprovalUnavailable,
			Data: map[string]any{"reason": "local-listen approval is not configured"},
		})
	}
	permission, prepareErr := e.prepareSecretPermission(ctx, tool, args, tc)
	if prepareErr != nil {
		return nil, prepareErr
	}
	result, err := e.localListenGate.Await(ctx, LocalListenAsk{
		SecretPermission: permission,
		SessionID:        tc.SessionID,
		ParentSessionID:  tc.ParentSessionID,
		ProjectID:        tc.ProjectID,
		ToolCallID:       tc.ToolCallID,
		ProjectDir:       tc.ActiveRootPath(),
		ToolName:         tool,
		Command:          commandsurface.PrimaryCommandLine(args, nil),
		Ports:            ports,
	})
	if err != nil {
		return nil, e.rejectApprovalErr(ctx, tool, tc.Agent, args, err)
	}
	if result.Denied {
		context := map[string]any{}
		if result.UserGuidance != "" {
			context[UserGuidanceKey] = result.UserGuidance
		}
		return nil, e.rejectBeforeInvoke(ctx, tool, tc.Agent, args, &ToolReject{
			Code: isolation.CodeLocalListenDenied,
			Data: context,
		})
	}
	if !result.Authorized {
		return nil, e.rejectBeforeInvoke(ctx, tool, tc.Agent, args, &ToolReject{
			Code: isolation.CodeApprovalUnavailable,
			Data: map[string]any{"reason": string(approvaloutcome.CodeApprovalExpired)},
		})
	}
	if result.SecretApproved {
		approveCapabilitySecretPermission(ctx, tc, permission, result.SecretAttestationID)
	}
	result.Ports = spawnPortNarrowing(result.Ports, ports)
	return &result, nil
}

// axisPortsCovered reports whether held ports reach every asked port.
// Empty held means any; an empty ask needs an unnarrowed hold.
func axisPortsCovered(held, asked []uint16) bool {
	if len(held) == 0 {
		return true
	}
	if len(asked) == 0 {
		return false
	}
	set := make(map[uint16]struct{}, len(held))
	for _, p := range held {
		set[p] = struct{}{}
	}
	for _, p := range asked {
		if _, ok := set[p]; !ok {
			return false
		}
	}
	return true
}
