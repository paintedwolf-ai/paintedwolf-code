package evidence

import (
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// BuildProgramObservation captures execution output without inferring source access.
func BuildProgramObservation(tool string, receipt *api.CommandProgramReceipt, content string) Record {
	if receipt == nil || strings.TrimSpace(receipt.ID) == "" || strings.TrimSpace(receipt.ParentReceiptID) == "" {
		return Record{}
	}
	switch tool {
	case "command", "command_output", "command_stop", "verify":
	default:
		return Record{}
	}
	rec := Record{Kind: "command", Shape: ShapeCommand, SourceTool: tool, Fidelity: FidelityOpaque}
	captureRecordBody(&rec, content)
	return rec
}
