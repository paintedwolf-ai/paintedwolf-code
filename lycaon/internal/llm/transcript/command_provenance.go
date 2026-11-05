package transcript

import "github.com/lycaon/lycaon/pkg/api"

// CommandResultProvenance retains receipts needed for command presentation.
type CommandResultProvenance struct {
	invocation api.InvocationReceipt
	program    api.CommandProgramReceipt
}

func commandReceiptBound(result *api.ToolResult) bool {
	if result == nil || result.Tool != "command" || result.CommandProgram == nil || result.Invocation == nil {
		return false
	}
	p, inv := result.CommandProgram, result.Invocation
	return p.ID != "" && inv.ID != "" && p.ParentReceiptID == inv.ID && inv.Tool == "command" && inv.ToolCallID != "" && inv.ToolCallID == result.ToolCallID && inv.Invoked
}

func CommandProvenance(result *api.ToolResult) *CommandResultProvenance {
	if !commandReceiptBound(result) {
		return nil
	}
	p, inv := result.CommandProgram, result.Invocation
	out := &CommandResultProvenance{invocation: *inv, program: api.CommandProgramReceipt{ID: p.ID, ParentReceiptID: p.ParentReceiptID, State: p.State, StopCode: p.StopCode}}
	if p.StatusCode != nil {
		n := *p.StatusCode
		out.program.StatusCode = &n
	}
	if inv.Failure != nil {
		failure := *inv.Failure
		out.invocation.Failure = &failure
	}
	if inv.Isolation != nil {
		isolation := *inv.Isolation
		out.invocation.Isolation = &isolation
	}
	return out
}

// Restore keeps compaction measurement and provider projection on the same facts.
func (p *CommandResultProvenance) Restore(result *api.ToolResult) {
	if p == nil || p.program.ID == "" || result == nil || result.Tool != "command" || result.ToolCallID != p.invocation.ToolCallID {
		return
	}
	invocation, program := p.invocation, p.program
	if program.StatusCode != nil {
		n := *program.StatusCode
		program.StatusCode = &n
	}
	if invocation.Failure != nil {
		failure := *invocation.Failure
		invocation.Failure = &failure
	}
	if invocation.Isolation != nil {
		isolation := *invocation.Isolation
		invocation.Isolation = &isolation
	}
	result.Invocation, result.CommandProgram = &invocation, &program
}
