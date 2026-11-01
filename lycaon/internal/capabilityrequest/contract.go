package capabilityrequest

import (
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

// ValidateCapabilityContract checks capabilities against the tool contract.
func ValidateCapabilityContract(tool string, contract toolcontract.Contract, args map[string]any) (result *toolrejection.ToolReject) {
	defer func() {
		if result == nil {
			return
		}
		if result.Data == nil {
			result.Data = map[string]any{}
		}
		result.CapabilityContract = &toolcontract.ToolCapabilitySnapshot{Tool: tool, Capabilities: contract.Capabilities}
		result.Data["capability_request_fields"] = contract.CapabilityRequestFields()
		result.Data["supports_local_listen"] = contract.Supports(toolcontract.CapabilityLocalListen)
		result.Data["supports_loopback_connect"] = contract.Supports(toolcontract.CapabilityLoopbackConnect)
	}()
	request, reject := ParseCapabilityRequest(args)
	if reject != nil {
		return reject
	}
	unsupported := func(name string) *toolrejection.ToolReject {
		return toolrejection.RejectInvalidArguments(isolation.CodeCapabilityRequestInvalid, map[string]any{
			"reason": "capability is not declared by the tool contract", "capability": name,
		})
	}
	if request != nil {
		if request.ProcessControl && !contract.Supports(toolcontract.CapabilityProcessControl) {
			return unsupported("process_control")
		}
		if request.CredentialStore && !contract.Supports(toolcontract.CapabilityCredentialStore) {
			return unsupported("credential_store")
		}
		if request.HostExecution && !contract.Supports(toolcontract.CapabilityHostExecution) {
			return unsupported("host_execution")
		}
		if len(request.HostResources) > 0 && !contract.Supports(toolcontract.CapabilityHostResource) {
			return unsupported("host_resource")
		}
		if len(request.SocketPaths) > 0 && !contract.Supports(toolcontract.CapabilitySocket) {
			return unsupported("socket")
		}
		if request.DirectIP != nil && !contract.Supports(toolcontract.CapabilityDirectIP) {
			return unsupported("direct_ip")
		}
		if request.LocalListen != nil && !contract.Supports(toolcontract.CapabilityLocalListen) {
			return unsupported("local_listen")
		}
		if request.LoopbackConnect != nil && !contract.Supports(toolcontract.CapabilityLoopbackConnect) {
			return unsupported("loopback_connect")
		}
		if request.WriteRoot != "" && !contract.Supports(toolcontract.CapabilityWriteRoot) {
			return unsupported("write_root")
		}
		if request.ReadPath != "" && !contract.Supports(toolcontract.CapabilityReadPath) {
			return unsupported("read_path")
		}
	}
	if _, present := args["terminal_capture"]; present && !contract.Supports(toolcontract.CapabilityTerminalCapture) {
		return unsupported("terminal_capture")
	}
	return nil
}
