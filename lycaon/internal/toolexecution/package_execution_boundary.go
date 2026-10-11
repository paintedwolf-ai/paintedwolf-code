package toolexecution

import (
	"github.com/lycaon/lycaon/internal/capabilityrequest"

	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func (e *Boundary) preflightPackageExecution(
	ctx context.Context,
	tool, profileID string,
	args map[string]any,
	tc *tools.ToolContext,
) error {
	request, reject := capabilityrequest.ParseCapabilityRequest(args)
	if reject != nil {
		return reject
	}
	if request != nil && request.HostExecution {
		tc.Files.PackageExecution = nil
		return nil
	}
	if tc.Invocation.Contract.Supports(toolcontract.CapabilityHeldTerminalInput) && e.packageExecution != nil {
		input, _ := args["input"].(string)
		if execution, matched := e.packageExecution.ClassifyTerminalInput(input); matched {
			return e.Rejections.rejectBeforeInvoke(ctx, tool, profileID, args, &toolrejection.ToolReject{
				Code: isolation.CodeRemotePackageRequiresFreshBoundary,
				Data: map[string]any{
					"manager": execution.Manager,
					"command": strings.TrimSpace(strings.ReplaceAll(input, "{Enter}", "")),
				},
			})
		}
	}
	if !tc.Invocation.Contract.Supports(toolcontract.CapabilityPackageExecution) {
		return nil
	}
	if e.packageExecutionErr != nil {
		return fmt.Errorf("package execution preflight unavailable: %w", e.packageExecutionErr)
	}
	if e.packageExecution == nil {
		return nil
	}
	packageExecution, err := e.packageExecution.Preflight(ctx, args, tc.ActiveRootPath())
	if err != nil {
		return fmt.Errorf("package execution preflight: %w", err)
	}
	tc.Files.PackageExecution = packageExecution
	return nil
}

func (e *Boundary) rejectPackageBoundaryWidening(
	ctx context.Context,
	tool, profileID string,
	args map[string]any,
	tc tools.ToolContext,
) error {
	if tc.Files.PackageExecution == nil {
		return nil
	}
	request, reject := capabilityrequest.ParseCapabilityRequest(args)
	if reject != nil {
		return e.Rejections.rejectBeforeInvoke(ctx, tool, profileID, args, reject)
	}
	blocked := packageBoundaryWidening(request, tc.Local.SocksProxyEnv)
	if len(blocked) == 0 {
		return nil
	}
	return e.Rejections.rejectBeforeInvoke(ctx, tool, profileID, args, &toolrejection.ToolReject{
		Code: isolation.CodeRemotePackageCapabilityDenied,
		Data: map[string]any{
			"manager":      tc.Files.PackageExecution.Manager,
			"command":      commandsurface.PrimaryCommandLine(args, nil),
			"capabilities": blocked,
		},
	})
}

func packageBoundaryWidening(request *capabilityrequest.CapabilityRequest, socksProxy bool) []string {
	var blocked []string
	if socksProxy {
		blocked = append(blocked, "socks_proxy")
	}
	if request == nil {
		return blocked
	}
	if len(request.HostResources) > 0 {
		blocked = append(blocked, "host_resources")
	}
	if len(request.SocketPaths) > 0 {
		blocked = append(blocked, "socket_paths")
	}
	if request.DirectIP != nil {
		blocked = append(blocked, "direct_ip")
	}
	if request.LocalListen != nil {
		blocked = append(blocked, "local_listen")
	}
	if request.LoopbackConnect != nil {
		blocked = append(blocked, "loopback_connect")
	}
	sort.Strings(blocked)
	return blocked
}
