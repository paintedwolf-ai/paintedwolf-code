package evidence

import (
	"fmt"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// BuildProgramObservation captures execution output without inferring source access.
func BuildProgramObservation(tool string, receipt *api.CommandProgramReceipt, content string) Record {
	if receipt == nil || strings.TrimSpace(receipt.ID) == "" || strings.TrimSpace(receipt.ParentReceiptID) == "" {
		return Record{}
	}
	if tool != "command_output" && tool != "command_stop" && !toolcontract.ProgramResultTool(tool) {
		return Record{}
	}
	rec := Record{Kind: "command", Shape: ShapeCommand, SourceTool: tool, Fidelity: FidelityOpaque}
	body := fmt.Sprintf("program_id: %s\nstate: %s", receipt.ID, receipt.State)
	if receipt.StatusCode != nil {
		body += fmt.Sprintf("\nstatus_code: %d", *receipt.StatusCode)
	}
	captureRecordBody(&rec, body+"\n\n"+content)
	return rec
}
