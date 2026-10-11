package toolrejection

import "strings"

const UserGuidanceKey = "user_guidance"

func AttachUserGuidance(reject *ToolReject, guidance string) {
	guidance = strings.TrimSpace(guidance)
	if reject == nil || guidance == "" {
		return
	}
	if reject.Data == nil {
		reject.Data = map[string]any{}
	}
	reject.Data[UserGuidanceKey] = guidance
}

func ApprovalPlanInvalid() *ToolReject {
	return &ToolReject{Code: "HOST_APPROVAL_PLAN_INVALID"}
}
