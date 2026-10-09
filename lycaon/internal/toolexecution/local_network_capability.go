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

// SetLocalNetworkGate wires combined listen+connect preflight.
func (e *Capabilities) SetLocalNetworkGate(gate tools.LocalNetworkGate) {
	if e != nil {
		e.localNetworkGate = gate
	}
}

// preflightLocalNetworkCapability combines simultaneous local network asks.
func (e *Capabilities) preflightLocalNetworkCapability(
	ctx context.Context,
	tool string,
	args map[string]any,
	tc tools.ToolContext,
) (listen *tools.LocalListenResult, connect *tools.LoopbackConnectResult, err error) {
	if !tc.Invocation.Contract.Supports(toolcontract.CapabilityLocalListen) &&
		!tc.Invocation.Contract.Supports(toolcontract.CapabilityLoopbackConnect) {
		return nil, nil, nil
	}
	capReq, reject := capabilityrequest.ParseCapabilityRequest(args)
	if reject != nil {
		return nil, nil, e.Rejections.rejectBeforeInvoke(ctx, tool, tc.Agent, args, reject)
	}
	if capReq == nil {
		return nil, nil, nil
	}
	listenWiden := capReq.LocalListen != nil && !tc.DirectIPRequested &&
		!(tc.LocalListenGranted && axisPortsCovered(tc.LocalListenPorts, capReq.LocalListen.Ports))
	connectWiden := capReq.LoopbackConnect != nil && !tc.DirectIPRequested &&
		!(tc.LoopbackConnectGranted && axisPortsCovered(tc.LoopbackConnectPorts, capReq.LoopbackConnect.Ports))
	if listenWiden && connectWiden && e.localNetworkGate != nil {
		permission, prepareErr := e.Secrets.prepareSecretPermission(ctx, tool, args, tc)
		if prepareErr != nil {
			return nil, nil, prepareErr
		}
		result, waitErr := e.localNetworkGate.AwaitCombined(ctx, tools.LocalNetworkAsk{
			SecretPermission: permission,
			SessionID:        tc.SessionID, ParentSessionID: tc.ParentSessionID,
			ProjectID: tc.ProjectID, ToolCallID: tc.ToolCallID,
			ProjectDir: tc.ActiveRootPath(), ToolName: tool,
			Command:      commandsurface.PrimaryCommandLine(args, nil),
			ListenPorts:  append([]uint16(nil), capReq.LocalListen.Ports...),
			ConnectPorts: append([]uint16(nil), capReq.LoopbackConnect.Ports...),
		})
		if waitErr != nil {
			return nil, nil, e.Approvals.rejectApprovalErr(ctx, tool, tc.Agent, args, waitErr)
		}
		if result.Denied {
			data := map[string]any{}
			if result.UserGuidance != "" {
				data[toolrejection.UserGuidanceKey] = result.UserGuidance
			}
			return nil, nil, e.Rejections.rejectBeforeInvoke(ctx, tool, tc.Agent, args, &toolrejection.ToolReject{
				Code: isolation.CodeLocalNetworkDenied, Data: data,
			})
		}
		if !result.Authorized {
			return nil, nil, e.Rejections.rejectBeforeInvoke(ctx, tool, tc.Agent, args, &toolrejection.ToolReject{
				Code: isolation.CodeApprovalUnavailable,
				Data: map[string]any{"reason": string(approvaloutcome.CodeApprovalExpired)},
			})
		}
		if result.SecretApproved {
			approveCapabilitySecretPermission(ctx, tc, permission)
		}
		return &tools.LocalListenResult{
				Authorized: true,
				Ports:      spawnPortNarrowing(result.ListenPorts, capReq.LocalListen.Ports),
			}, &tools.LoopbackConnectResult{
				Authorized: true,
				Ports:      spawnPortNarrowing(result.ConnectPorts, capReq.LoopbackConnect.Ports),
			}, nil
	}
	listen, err = e.preflightLocalListenCapability(ctx, tool, args, tc)
	if err != nil {
		return nil, nil, err
	}
	connect, err = e.preflightLoopbackConnectCapability(ctx, tool, args, tc)
	return listen, connect, err
}

// spawnPortNarrowing applies declared ports to the current spawn.
func spawnPortNarrowing(lease, asked []uint16) []uint16 {
	if len(asked) > 0 {
		return asked
	}
	return lease
}
