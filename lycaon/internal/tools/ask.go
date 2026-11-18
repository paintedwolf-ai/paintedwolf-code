package tools

import (
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

const AskModeActiveCode = "SESSION_ASK_MODE_ACTIVE"

// AllowedInAsk checks the compiled native contract or the connected tool's declaration.
func AllowedInAsk(meta ToolMeta) bool {
	if meta.IsMCP() {
		return meta.ReadOnlyHint
	}
	contract, ok := toolcontract.Lookup(meta.Name)
	return ok && contract.AskAllowed
}

func AskModeReject(tool string) *toolrejection.ToolReject {
	return &toolrejection.ToolReject{Code: AskModeActiveCode, Data: map[string]any{"tool": tool}}
}
