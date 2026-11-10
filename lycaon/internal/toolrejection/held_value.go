package toolrejection

import (
	"errors"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/secretmatch"
)

func HeldHandOffReject(surface string, err error) *ToolReject {
	switch {
	case errors.Is(err, secretcap.ErrRevoked):
		return &ToolReject{Code: "SECRET_REFERENCE_REVOKED", Retryable: true}
	case errors.Is(err, secretcap.ErrAgentUseExpired):
		return &ToolReject{Code: "SECRET_REFERENCE_AGENT_USE_EXPIRED", Retryable: true}
	case errors.Is(err, secretcap.ErrNotVisible):
		return &ToolReject{Code: "SECRET_REFERENCE_OUT_OF_SCOPE", Retryable: true}
	case errors.Is(err, secretcap.ErrNotFound):
		return &ToolReject{Code: "SECRET_REFERENCE_NOT_FOUND", Retryable: true}
	}
	stage := secretmatch.FaultStageHeldUnreleased
	if errors.Is(err, secretcap.ErrVaultLocked) {
		stage = secretmatch.FaultStageVaultLocked
	}
	return &ToolReject{Code: OutboundSecretScreenFailedCode, Data: map[string]any{
		"surface": surface, "rule_id": secretmatch.ManagedRuleID, "shape": "Protected value",
		"fault_stage": stage,
	}}
}
