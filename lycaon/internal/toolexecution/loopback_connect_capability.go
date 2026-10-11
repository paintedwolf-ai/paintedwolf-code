package toolexecution

import (
	"github.com/lycaon/lycaon/internal/capabilityrequest"

	"context"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"

	"github.com/lycaon/lycaon/internal/approvaloutcome"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// SetLoopbackConnectGate wires pre-spawn local connection review.
func (e *Capabilities) SetLoopbackConnectGate(gate tools.LoopbackConnectGate) {
	if e != nil {
		e.loopbackConnectGate = gate
	}
}

func (e *Capabilities) preflightLoopbackConnectCapability(
	ctx context.Context,
	tool string,
	args map[string]any,
	tc tools.ToolContext,
) (*tools.LoopbackConnectResult, error) {
	if !tc.Invocation.Contract.Supports(toolcontract.CapabilityLoopbackConnect) {
		return nil, nil
	}
	capReq, reject := capabilityrequest.ParseCapabilityRequest(args)
	if reject != nil {
		return nil, e.Rejections.rejectBeforeInvoke(ctx, tool, tc.Identity.Agent, args, reject)
	}
	if capReq == nil || capReq.LoopbackConnect == nil {
		return nil, nil
	}
	ports := append([]uint16(nil), capReq.LoopbackConnect.Ports...)
	if tc.Effects.Secrets.LocalConnectionsCovered(ports) {
		return &tools.LoopbackConnectResult{Authorized: true, Ports: ports}, nil
	}
	// Direct IP already carries local outbound authority.
	if tc.Direct.DirectIPRequested {
		return &tools.LoopbackConnectResult{Authorized: true}, nil
	}
	if tc.Local.LoopbackConnectGranted && axisPortsCovered(tc.Local.LoopbackConnectPorts, ports) {
		return &tools.LoopbackConnectResult{Authorized: true, Ports: spawnPortNarrowing(tc.Local.LoopbackConnectPorts, ports)}, nil
	}
	if e.loopbackConnectGate == nil {
		return nil, e.Rejections.rejectBeforeInvoke(ctx, tool, tc.Identity.Agent, args, &toolrejection.ToolReject{
			Code: isolation.CodeApprovalUnavailable,
			Data: map[string]any{"reason": "loopback-connect approval is not configured"},
		})
	}
	permission, prepareErr := e.Secrets.prepareSecretPermission(ctx, tool, args, tc)
	if prepareErr != nil {
		return nil, prepareErr
	}
	result, err := e.loopbackConnectGate.Await(ctx, tools.LoopbackConnectAsk{
		SecretPermission: permission,
		SessionID:        tc.Identity.SessionID, ParentSessionID: tc.Identity.ParentSessionID,
		ProjectID: tc.Identity.ProjectID, ToolCallID: tc.Identity.ToolCallID,
		ProjectDir: tc.ActiveRootPath(), ToolName: tool,
		Command: commandsurface.PrimaryCommandLine(args, nil), Ports: ports,
	})
	if err != nil {
		return nil, e.Approvals.rejectApprovalErr(ctx, tool, tc.Identity.Agent, args, err)
	}
	if result.Denied {
		data := map[string]any{}
		if result.UserGuidance != "" {
			data[toolrejection.UserGuidanceKey] = result.UserGuidance
		}
		return nil, e.Rejections.rejectBeforeInvoke(ctx, tool, tc.Identity.Agent, args, &toolrejection.ToolReject{
			Code: isolation.CodeLoopbackConnectDenied, Data: data,
		})
	}
	if !result.Authorized {
		return nil, e.Rejections.rejectBeforeInvoke(ctx, tool, tc.Identity.Agent, args, &toolrejection.ToolReject{
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
