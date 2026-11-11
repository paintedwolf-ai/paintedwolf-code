package hitl

const ApprovalGrantCategoryExecutionCapability = "execution_capability"

// ExecutionCapabilityForAction uses the resolved boundary and process operation.
func ExecutionCapabilityForAction(action ProposedAction) string {
	caps := ExecutionCapabilitiesForAction(action)
	if len(caps) == 0 {
		return ""
	}
	return caps[0]
}

// ExecutionCapabilitiesForAction lists every execution capability on the action.
func ExecutionCapabilitiesForAction(action ProposedAction) []string {
	var out []string
	if action.Execution.Contained.HostExecution {
		out = append(out, "host_execution")
	}
	if action.Execution.Contained.ProcessControl {
		out = append(out, "process_control")
	}
	if action.Execution.Contained.CredentialStore {
		out = append(out, "credential_store")
	}
	switch action.Execution.ProcessAccess {
	case "list":
		out = append(out, "process_list")
	case "signal":
		out = append(out, "process_signal")
	}
	return out
}

func ExecutionCapabilityCoverage(capability string) string {
	switch capability {
	case "host_execution":
		return "commands outside the sandbox in this chat, including sudo and setuid programs; filesystem, network, and process effects are unobserved"
	case "process_control":
		return "commands signaling outside their sandbox in this chat; filesystem and network restrictions remain applied"
	case "credential_store":
		return "commands reading the OS credential store in this chat; filesystem, write, and network restrictions remain applied"
	case "process_list":
		return "host process inspection in this chat, without process arguments or environment"
	case "process_signal":
		return "signaling host processes in this chat; every call still resolves exact process instances"
	default:
		return ""
	}
}
