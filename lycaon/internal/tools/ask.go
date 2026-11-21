package tools

import (
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

const AskModeActiveCode = "SESSION_ASK_MODE_ACTIVE"

var commandReadOnlyPrunedFields = []string{
	"capability_request",
	"secret_use",
	"snapshot_capture",
	"socks_proxy",
	"terminal_capture",
	"verification",
}

// AllowedInAsk checks the compiled native contract or the connected tool's declaration.
func AllowedInAsk(meta ToolMeta) bool {
	if meta.IsMCP() {
		return meta.ReadOnlyHint
	}
	switch meta.Name {
	case "command", "command_output", "command_stop":
		return true
	}
	contract, ok := toolcontract.Lookup(meta.Name)
	return ok && contract.AskAllowed
}

func AskModeReject(tool string) *toolrejection.ToolReject {
	return &toolrejection.ToolReject{Code: AskModeActiveCode, Data: map[string]any{"tool": tool}}
}

// AskModeCommandFieldReject refuses command envelope parameters that are pruned
// from the read-only Ask schema. It checks key presence only and omits caller
// values from the rejection payload.
func AskModeCommandFieldReject(tool string, args map[string]any) *toolrejection.ToolReject {
	if tool != "command" || len(args) == 0 {
		return nil
	}
	for _, field := range commandReadOnlyPrunedFields {
		if _, present := args[field]; present {
			return &toolrejection.ToolReject{
				Code: AskModeActiveCode,
				Data: map[string]any{"tool": tool, "field": field},
			}
		}
	}
	return nil
}
