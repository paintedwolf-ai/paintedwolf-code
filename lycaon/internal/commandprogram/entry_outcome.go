package commandprogram

import "github.com/lycaon/lycaon/internal/toolcontract"

func failedEntry(invocation toolcontract.OperationInvocation, cause error) ProgramOutcome {
	result := ownerResult(cause)
	kind := StopOwner
	switch result.Terminal {
	case toolcontract.ProducerRefused:
		kind = StopAuthority
	case toolcontract.ProducerInterrupted:
		kind = StopInterrupted
	default:
		// Other terminals stay with the owner.
	}
	return ProgramOutcome{Status: result.Status, Stop: &HostStop{Kind: kind, ExecutionID: invocation.Identity.ExecutionID, Result: result}}
}
