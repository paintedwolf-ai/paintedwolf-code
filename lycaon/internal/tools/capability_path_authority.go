package tools

import (
	"github.com/lycaon/lycaon/internal/capabilityrequest"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

// ValidateCapabilityPathAuthority uses the same session boundary as execution.
func ValidateCapabilityPathAuthority(args map[string]any, scratch string) *toolrejection.ToolReject {
	request, reject := capabilityrequest.ParseCapabilityRequest(args)
	if reject != nil || request == nil {
		return reject
	}
	for _, capability := range []struct {
		name, path string
		write      bool
	}{
		{"read_path", request.ReadPath, false}, {"write_root", request.WriteRoot, true},
	} {
		if capability.path != "" && confine.ControlPlanePathDenied(capability.path, capability.write, scratch) {
			return &toolrejection.ToolReject{Code: isolation.CodeControlPlaneDenied, Data: map[string]any{"path": capability.path, "capability": capability.name}}
		}
	}
	return nil
}
