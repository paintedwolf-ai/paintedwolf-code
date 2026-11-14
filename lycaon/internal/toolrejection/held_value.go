package toolrejection

import (
	"errors"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/secretmatch"
)

func HeldHandOffReject(surface string, err error) *ToolReject {
	if reject, ok := SecretCapabilityReject(err); ok {
		return reject
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
