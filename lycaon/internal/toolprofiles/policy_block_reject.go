package toolprofiles

import (
	"strings"

	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/pkg/api"
)

// PolicyBlockReject is the refusal a blocked decision hands the executor.
func PolicyBlockReject(decision *platform.PolicyDecision, tool, profileID string) *toolrejection.ToolReject {
	code := ""
	var data map[string]any
	if decision != nil {
		code = strings.TrimSpace(decision.RejectCode)
		data = decision.RejectData
	}
	if code == "" {
		code = "TOOL_PROFILE_DENIED"
	}
	if data == nil {
		data = map[string]any{"tool": tool, "profile": profileID}
	}
	return &toolrejection.ToolReject{Code: code, Data: data, FailureClass: api.FailureClassPolicyRejection}
}

func policyBlockObservation(eval platform.PolicyContext) *toolrejection.ToolReject {
	data := map[string]any{
		"tool":    eval.ToolName,
		"profile": eval.ProfileID,
	}
	code := "TOOL_PROFILE_DENIED"
	if eval.ProfileID == toolcontract.CoordinatorProfileID {
		code = "COORDINATOR_TOOL_DENIED"
	}
	return &toolrejection.ToolReject{Code: code, Data: data}
}
