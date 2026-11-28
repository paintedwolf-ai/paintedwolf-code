package toolexecution

import (
	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

// approvalRefusalReject maps a refused approval to its agent-public code. A
// person's decline keeps deniedCode and their direction; any other refusal is
// SANDBOX_APPROVAL_UNAVAILABLE with facts naming what ended the request.
func approvalRefusalReject(deniedCode string, cause isolation.PathDenialCause, resolvedBy, guidance string) *toolrejection.ToolReject {
	if cause == isolation.PathDenialUserDeclined || cause == isolation.PathDenialPriorUserDeclined {
		data := map[string]any{}
		if guidance != "" {
			data[toolrejection.UserGuidanceKey] = guidance
		}
		return &toolrejection.ToolReject{Code: deniedCode, Data: data}
	}
	data := map[string]any{}
	switch resolvedBy {
	case authzledger.ResolvedByPolicy, authzledger.ResolvedBySystemDeny:
		data["approval_refused_automatically"] = true
	case authzledger.ResolvedByUserStop:
		data["approval_stopped_by_user"] = true
	case authzledger.ResolvedByHostStop:
		data["approval_stopped_by_host"] = true
	case authzledger.ResolvedByExpiry:
		data["approval_expired"] = true
	}
	return &toolrejection.ToolReject{Code: isolation.CodeApprovalUnavailable, Data: data}
}
