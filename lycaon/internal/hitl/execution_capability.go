package hitl

const ApprovalGrantCategoryExecutionCapability = "execution_capability"

// ExecutionCapabilityForAction uses the resolved boundary and process operation.
func ExecutionCapabilityForAction(action ProposedAction) string {
	if action.Execution.Contained.HostExecution {
		return "host_execution"
	}
	if action.Execution.Contained.ProcessControl {
		return "process_control"
	}
	switch action.Execution.ProcessAccess {
	case "list":
		return "process_list"
	case "signal":
		return "process_signal"
	}
	return ""
}

func ExecutionCapabilityCoverage(capability string) string {
	switch capability {
	case "host_execution":
		return "commands outside the sandbox in this chat, including sudo and setuid programs; filesystem, network, and process effects are unobserved"
	case "process_control":
		return "commands signaling outside their sandbox in this chat; filesystem and network restrictions remain applied"
	case "process_list":
		return "host process inspection in this chat, without process arguments or environment"
	case "process_signal":
		return "signaling host processes in this chat; every call still resolves exact process instances"
	default:
		return ""
	}
}
