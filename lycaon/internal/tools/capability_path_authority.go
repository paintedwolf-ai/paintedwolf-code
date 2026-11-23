package tools

import (
	"github.com/lycaon/lycaon/internal/capabilityrequest"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

// ValidateCapabilityPathAuthority uses the same session boundary as execution.
func ValidateCapabilityPathAuthority(args map[string]any, scratch, output string) *toolrejection.ToolReject {
	request, reject := capabilityrequest.ParseCapabilityRequest(args)
	if reject != nil || request == nil {
		return reject
	}
	for _, path := range request.ReadPaths {
		if confine.ControlPlanePathDenied(path, false, scratch, output) {
			return &toolrejection.ToolReject{Code: isolation.CodeControlPlaneDenied, Data: map[string]any{"path": path, "capability": "read_path"}}
		}
	}
	if root := request.WriteRoot; root != "" && confine.ControlPlanePathDenied(root, true, scratch, output) {
		return &toolrejection.ToolReject{Code: isolation.CodeControlPlaneDenied, Data: map[string]any{"path": root, "capability": "write_root"}}
	}
	return nil
}
