package feedback

import "strings"

// HumanApprovalLeaf is the review-bar gate. Approve satisfies it.
const HumanApprovalLeaf = "human_approval"

const evidencePassedPrefix = "evidence_passed:"

const workflowAdvanceTool = "workflow_advance"

func IsWorkflowAdvance(tool string) bool {
	return strings.EqualFold(strings.TrimSpace(tool), workflowAdvanceTool)
}

// ActionableFailedLeaves is the failed-leaf set for this tool's gate banner.
// workflow_advance sees every leaf; other tools drop HITL and evidence_passed leaves.
func ActionableFailedLeaves(tool string, leaves []string) []string {
	if IsWorkflowAdvance(tool) {
		return nonEmptyStrings(leaves)
	}
	out := make([]string, 0, len(leaves))
	for _, leaf := range leaves {
		leaf = strings.TrimSpace(leaf)
		if leaf == "" || leaf == HumanApprovalLeaf || strings.HasPrefix(leaf, evidencePassedPrefix) {
			continue
		}
		out = append(out, leaf)
	}
	return out
}
