package tools

import (
	"context"

	"github.com/lycaon/lycaon/internal/approvaloutcome"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// SetLoopbackConnectGate wires pre-spawn local connection review.
func (e *DefaultToolExecutor) SetLoopbackConnectGate(gate LoopbackConnectGate) {
	if e != nil {
		e.loopbackConnectGate = gate
	}
}

func (e *DefaultToolExecutor) preflightLoopbackConnectCapability(
	ctx context.Context,
	tool string,
	args map[string]any,
	tc ToolContext,
) (*LoopbackConnectResult, error) {
	if !tc.Invocation.Contract.Supports(toolcontract.CapabilityLoopbackConnect) {
		return nil, nil
	}
	capReq, reject := ParseCapabilityRequest(args)
	if reject != nil {
		return nil, e.rejectBeforeInvoke(ctx, tool, tc.Agent, args, reject)
	}
	if capReq == nil || capReq.LoopbackConnect == nil {
		return nil, nil
	}
	ports := append([]uint16(nil), capReq.LoopbackConnect.Ports...)
	if tc.Secrets.LocalConnectionsCovered(ports) {
		return &LoopbackConnectResult{Authorized: true, Ports: ports}, nil
	}
	// Direct IP already carries local outbound authority.
	if tc.DirectIPRequested {
		return &LoopbackConnectResult{Authorized: true}, nil
	}
	if tc.LoopbackConnectGranted && axisPortsCovered(tc.LoopbackConnectPorts, ports) {
		return &LoopbackConnectResult{Authorized: true, Ports: spawnPortNarrowing(tc.LoopbackConnectPorts, ports)}, nil
	}
	if e.loopbackConnectGate == nil {
		return nil, e.rejectBeforeInvoke(ctx, tool, tc.Agent, args, &ToolReject{
			Code: isolation.CodeApprovalUnavailable,
			Data: map[string]any{"reason": "loopback-connect approval is not configured"},
		})
	}
	permission, prepareErr := e.prepareSecretPermission(ctx, tool, args, tc)
	if prepareErr != nil {
		return nil, prepareErr
	}
	result, err := e.loopbackConnectGate.Await(ctx, LoopbackConnectAsk{
		SecretPermission: permission,
		SessionID:        tc.SessionID, ParentSessionID: tc.ParentSessionID,
		ProjectID: tc.ProjectID, ToolCallID: tc.ToolCallID,
		ProjectDir: tc.ActiveRootPath(), ToolName: tool,
		Command: commandsurface.PrimaryCommandLine(args, nil), Ports: ports,
	})
	if err != nil {
		return nil, e.rejectApprovalErr(ctx, tool, tc.Agent, args, err)
	}
	if result.Denied {
		data := map[string]any{}
		if result.UserGuidance != "" {
			data[UserGuidanceKey] = result.UserGuidance
		}
		return nil, e.rejectBeforeInvoke(ctx, tool, tc.Agent, args, &ToolReject{
			Code: isolation.CodeLoopbackConnectDenied, Data: data,
		})
	}
	if !result.Authorized {
		return nil, e.rejectBeforeInvoke(ctx, tool, tc.Agent, args, &ToolReject{
			Code: isolation.CodeApprovalUnavailable,
			Data: map[string]any{"reason": string(approvaloutcome.CodeApprovalExpired)},
		})
	}
	if result.SecretApproved {
		approveCapabilitySecretPermission(ctx, tc, permission)
	}
	result.Ports = spawnPortNarrowing(result.Ports, ports)
	return &result, nil
}
