package tools

import (
	"context"
	"sort"

	"github.com/lycaon/lycaon/internal/hostresources"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/oar"
)

// HostResourceConnectionSource resolves resource IDs into connection requests.
type HostResourceConnectionSource func(
	ctx context.Context,
	ids []string,
	project hostresources.ProjectContext,
	surfaces []hostresources.ExecutionSurface,
) (hostresources.ActionResolution, []string)

// SetHostResourceConnectionSource wires host-resource availability into process starts.
func (e *DefaultToolExecutor) SetHostResourceConnectionSource(source HostResourceConnectionSource) {
	if e != nil {
		e.hostResourceSource = source
	}
}

func (e *DefaultToolExecutor) expandHostResourceConnections(
	ctx context.Context,
	tool string,
	args map[string]any,
	tc ToolContext,
) (map[string]any, *hostresources.ActionResolution, error) {
	request, reject := ParseCapabilityRequest(args)
	if reject != nil {
		return nil, nil, e.rejectBeforeInvoke(ctx, tool, tc.Agent, args, reject)
	}
	if request == nil || len(request.HostResources) == 0 {
		return args, nil, nil
	}
	if e.hostResourceSource == nil {
		return nil, nil, e.rejectBeforeInvoke(ctx, tool, tc.Agent, args, &ToolReject{
			Code: isolation.CodeCapabilityRequestInvalid,
			Data: map[string]any{
				"reason":         "host resource catalog is unavailable",
				"host_resources": request.HostResources,
			},
		})
	}
	resolution, unmet := e.hostResourceSource(
		ctx,
		request.HostResources,
		hostresources.ProjectContext{ID: tc.ProjectID, Dir: tc.ActiveRootPath()},
		[]hostresources.ExecutionSurface{hostresources.SurfaceProcessExec},
	)
	if len(unmet) > 0 {
		return nil, nil, e.rejectBeforeInvoke(ctx, tool, tc.Agent, args, &ToolReject{
			Code: isolation.CodeCapabilityRequestInvalid,
			Data: map[string]any{
				"reason":         "requested host resources are unavailable",
				"host_resources": unmet,
			},
		})
	}
	if len(resolution.Deny) > 0 {
		if e.blockPlane != nil && e.blockPlane.Enforces(oar.AnchorToolRejected) {
			if err := e.blockPlane.Evaluate(ctx, oar.AnchorToolRejected, tool, tc.Agent, args, func(gc *oar.GuardContext) error {
				observeHostResourceResolution(gc, resolution)
				gc.PutRejectData("HOST_RESOURCE_POLICY_DENIED", map[string]any{"host_resources": resolution.Deny})
				return nil
			}); err != nil {
				return nil, &resolution, err
			}
		}
		return nil, &resolution, e.renderReject(&ToolReject{
			Code:        "HOST_RESOURCE_POLICY_DENIED",
			Observation: "host_resource_policy_denied",
			Data:        map[string]any{"host_resources": resolution.Deny, "action_host_resource_denials": resolution.Deny},
		})
	}
	merged, reject := mergeHostResourceConnections(args, resolution.Connections)
	if reject != nil {
		return nil, &resolution, e.rejectBeforeInvoke(ctx, tool, tc.Agent, args, reject)
	}
	return merged, &resolution, nil
}

func observeHostResourceResolution(gc *oar.GuardContext, resolution hostresources.ActionResolution) {
	if gc == nil {
		return
	}
	gc.ActionHostResources = sortedHostResourceIDs(resolution.States)
	gc.ActionHostResourceDenials = append([]string(nil), resolution.Deny...)
	sort.Strings(gc.ActionHostResourceDenials)
	gc.HostResourceStatus = make(map[string]string, len(resolution.States))
	gc.HostResourcePolicy = make(map[string]string, len(resolution.States))
	for id, state := range resolution.States {
		gc.HostResourceStatus[id] = string(state.Status)
		gc.HostResourcePolicy[id] = string(state.Access)
	}
}

func mergeHostResourceConnections(
	args map[string]any,
	connections hostresources.ConnectionRequest,
) (map[string]any, *ToolReject) {
	out := make(map[string]any, len(args))
	for key, value := range args {
		out[key] = value
	}
	raw, _ := args["capability_request"].(map[string]any)
	capabilityRequest := make(map[string]any, len(raw)+2)
	for key, value := range raw {
		capabilityRequest[key] = value
	}
	var socketPaths []string
	for _, service := range connections.LocalServices {
		if service.Transport != hostresources.LocalServiceUnixSocket {
			return nil, &ToolReject{
				Code: isolation.CodeCapabilityRequestInvalid,
				Data: map[string]any{
					"reason":    "host-resource route is unsupported by this process executor",
					"transport": service.Transport,
				},
			}
		}
		socketPaths = append(socketPaths, service.Target)
	}
	if len(socketPaths) > 0 {
		existing, _ := stringList(capabilityRequest["socket_paths"])
		paths := append(append([]string(nil), existing...), socketPaths...)
		sort.Strings(paths)
		capabilityRequest["socket_paths"] = paths
	}
	if len(connections.DirectDestinations) > 0 {
		direct, _ := capabilityRequest["direct_ip"].(map[string]any)
		directCopy := make(map[string]any, len(direct)+1)
		for key, value := range direct {
			directCopy[key] = value
		}
		existing, _ := stringList(directCopy["declared_destinations"])
		destinations := append(append([]string(nil), existing...), connections.DirectDestinations...)
		sort.Strings(destinations)
		directCopy["declared_destinations"] = destinations
		capabilityRequest["direct_ip"] = directCopy
	}
	out["capability_request"] = capabilityRequest
	return out, nil
}
