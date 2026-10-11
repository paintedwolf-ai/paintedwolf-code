package toolsecrets

import (
	"github.com/lycaon/lycaon/internal/toolapproval"

	"github.com/lycaon/lycaon/internal/hitl"
)

const OutboundSecretExplainTool = "outbound_secret"

// FallbackSecretExplanation builds approval copy without a catalog entry.
func FallbackSecretExplanation(screen *hitl.SecretScreen) toolapproval.ApprovalExplanation {
	return toolapproval.ApprovalExplanation{
		What:      hitl.SecretImpact(screen),
		Who:       "An external destination would receive this content if you allow this request.",
		IfWrong:   "Denying only blocks this request; the secret stays on your machine.",
		AllowLine: "sending this outbound request despite the match",
	}
}
