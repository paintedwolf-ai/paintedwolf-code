package tools

import (
	"github.com/lycaon/lycaon/internal/hitl"
)

const outboundSecretExplainTool = "outbound_secret"

// fallbackSecretExplanation builds approval copy without a catalog entry.
func fallbackSecretExplanation(screen *hitl.SecretScreen) ApprovalExplanation {
	return ApprovalExplanation{
		What:      hitl.SecretImpact(screen),
		Who:       "An external destination would receive this content if you allow this request.",
		IfWrong:   "Denying only blocks this request; the secret stays on your machine.",
		AllowLine: "sending this outbound request despite the match",
	}
}
