package tools

import (
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

// CommandOperationReject identifies an exact operation name without activating a tool.
func CommandOperationReject(name string) *toolrejection.ToolReject {
	if !toolcontract.IsCommandOperation(name) {
		return nil
	}
	return &toolrejection.ToolReject{Code: "TOOL_COMMAND_OPERATION", Data: map[string]any{"need": name}}
}
