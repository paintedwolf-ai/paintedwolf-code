package inject

import (
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// MaxRejectedOperationStepAge is the number of subsequent settled assistant
// tool batches after which a reminder leaves the prompt. Rows live at ages 0
// through 3.
const MaxRejectedOperationStepAge = 4

type reminderState uint8

const (
	reminderLive reminderState = iota
	reminderResolved
	reminderExpired
)

// executionStateRow is one receipt as the projection reads it. Path, Command,
// Cwd, Resolvable and the source fields hold rendered values; the raw fields
// carry identity for linkage.
type executionStateRow struct {
	Tool, CallID, Status, ArgsDigest, Code, SourceVerdict string
	Invoked                                               bool
	Path, Command, Cwd, Resolvable                        string
	SourceRevision, SourceRootDigest                      string
	ChecklistAfter, ToolRequestAfter                      bool

	rawPath, rawCommand, rawCwd, rawRevision, rawRoot string
	// priorRevision is the newest generation stamped on any earlier receipt.
	priorRevision            string
	completed, failed, check bool
	Age                      int
	State                    reminderState
}

// executionStateProjection is what one prompt may say about recent receipts.
// Retired names the reminders that left and why; it never reaches the model.
type executionStateProjection struct {
	Operations, Executions               []executionStateRow
	OperationsOmitted, ExecutionsOmitted int
	Recovery                             bool
	Retired                              []executionStateRow
}

// projectExecutionState is a pure read of a canonical window, so ages depend
// only on durable rows and never on what the prompt history retained.
func projectExecutionState(window []api.Message, declared string) executionStateProjection {
	messages := window[api.UserIntentBoundary(window):]
	ages := executionBatchAges(messages)
	var operations, executions []executionStateRow
	var retired []executionStateRow
	latestRevision := ""
	for _, message := range messages {
		result := message.ToolResult
		if message.Role != api.MessageRoleTool || !pairedExecutionReceipt(result) {
			continue
		}
		receipt := result.Invocation
		row := newExecutionStateRow(result, ages[executionBatchID(result)], declared)
		row.priorRevision = latestRevision
		if receipt.SourceRevision != "" {
			latestRevision = receipt.SourceRevision
		}
		if row.completed && row.Invoked {
			notePrerequisite(operations, result.Tool)
		}
		if rejectedChange(result) {
			operations = append(refreshedOperations(operations, row), row)
		} else if row.completed && row.Invoked {
			var resolved []executionStateRow
			operations, resolved = withoutRetiredOperations(operations, row)
			retired = append(retired, resolved...)
		}
		if isExecutionTool(result.Tool) {
			executions = append(refreshedExecutions(executions, row), row)
		}
	}
	retired = append(retired, resolveExecutions(executions)...)

	projection := executionStateProjection{Retired: retired}
	var liveOperations, liveExecutions []executionStateRow
	for _, row := range operations {
		if row.Age >= MaxRejectedOperationStepAge {
			row.State = reminderExpired
			projection.Retired = append(projection.Retired, row)
			continue
		}
		liveOperations = append(liveOperations, row)
	}
	for _, row := range executions {
		if row.Age >= MaxRejectedOperationStepAge {
			if row.failed && row.State == reminderLive {
				row.State = reminderExpired
				projection.Retired = append(projection.Retired, row)
			}
			continue
		}
		liveExecutions = append(liveExecutions, row)
		projection.Recovery = projection.Recovery || row.failed && row.State == reminderLive
	}
	projection.Operations = lastExecutionRows(liveOperations)
	projection.OperationsOmitted = len(liveOperations) - len(projection.Operations)
	projection.Executions = boundedExecutionRows(liveExecutions)
	projection.ExecutionsOmitted = len(liveExecutions) - len(projection.Executions)
	return projection
}

func newExecutionStateRow(result *api.ToolResult, age int, declared string) executionStateRow {
	receipt := result.Invocation
	row := executionStateRow{
		Tool: receipt.Tool, CallID: receipt.ToolCallID, Status: string(receipt.Status),
		Invoked: receipt.Invoked, ArgsDigest: receipt.ArgsDigest, SourceVerdict: receipt.SourceVerdict,
		SourceRevision: quotedExecutionValue(receipt.SourceRevision), SourceRootDigest: quotedExecutionValue(receipt.SourceRootDigest),
		rawRevision: receipt.SourceRevision, rawRoot: receipt.SourceRootDigest, Age: age,
		completed: receipt.Status == api.InvocationStatusCompleted,
	}
	if receipt.Failure != nil {
		row.Code = receipt.Failure.Code
	}
	if program, ok := rejectedCommandProgram(result); ok {
		row.Status = string(api.InvocationStatusRejected)
		row.completed = false
		if row.Code == "" {
			row.Code = program.StopCode
		}
	}
	row.rawPath, _ = result.ToolArgs["path"].(string)
	row.rawCommand, _ = result.ToolArgs["command"].(string)
	row.rawCwd, _ = result.ToolArgs["cwd"].(string)
	row.Path, row.Command, row.Cwd = quotedExecutionValue(row.rawPath), quotedExecutionValue(row.rawCommand), quotedExecutionValue(row.rawCwd)
	requested, _ := result.ToolArgs["verification"].(bool)
	row.check = receipt.Tool == "verify" || requested
	if receipt.Tool == "verify" && row.rawCommand == "" && result.ToolArgs["pipeline"] == nil {
		// An empty verify command runs the declared project check.
		row.rawCommand = declared
	}
	for _, feedback := range result.Feedback {
		if feedback.Code == "COMMAND_NOT_FOUND" {
			value, _ := feedback.Details["resolvable"].(string)
			row.Resolvable = quotedExecutionValue(value)
		}
	}
	row.failed = failedExecution(result)
	return row
}

func isExecutionTool(tool string) bool {
	switch tool {
	case "command", "verify", "terminal_open", "terminal_write", "command_output", "terminal_read":
		return true
	}
	return false
}

// failedExecution reads one receipt. A poll without a verdict says nothing about
// the work it polled.
func failedExecution(result *api.ToolResult) bool {
	if _, ok := rejectedCommandProgram(result); ok {
		return true
	}
	receipt := result.Invocation
	if !isExecutionTool(result.Tool) {
		return false
	}
	if isPollTool(result.Tool) && receipt.SourceVerdict == "" {
		return false
	}
	return !receipt.Invoked || receipt.Status != api.InvocationStatusCompleted || receipt.Failure != nil || receipt.SourceVerdict == api.SourceVerdictFailed
}

func notePrerequisite(operations []executionStateRow, tool string) {
	for i := range operations {
		switch {
		case tool == "update_progress" && (operations[i].Code == "PROGRESS_MISSING" || operations[i].Code == "PROGRESS_ITEM_NOT_CLOSED" || operations[i].Code == "PROGRESS_RECONCILIATION_REQUIRED"):
			operations[i].ChecklistAfter = true
		case tool == "request_tools" && operations[i].Code == "TOOL_NOT_OFFERED":
			operations[i].ToolRequestAfter = true
		}
	}
}

// refreshedOperations drops the older reminder a repeated rejection replaces.
func refreshedOperations(operations []executionStateRow, repeated executionStateRow) []executionStateRow {
	if repeated.ArgsDigest == "" {
		return operations
	}
	kept := operations[:0]
	for _, row := range operations {
		if row.Tool != repeated.Tool || row.ArgsDigest != repeated.ArgsDigest {
			kept = append(kept, row)
		}
	}
	return kept
}

// withoutRetiredOperations resolves rows linked to a completed call: the same
// operation by tool and argument digest, or a change to the same path.
func withoutRetiredOperations(operations []executionStateRow, completed executionStateRow) (kept, resolved []executionStateRow) {
	kept = operations[:0]
	for _, row := range operations {
		samePath := isChangeTool(completed.Tool) && isChangeTool(row.Tool) && row.rawPath != "" && row.rawPath == completed.rawPath
		sameOperation := completed.ArgsDigest != "" && row.Tool == completed.Tool && row.ArgsDigest == completed.ArgsDigest
		if samePath || sameOperation {
			row.State = reminderResolved
			resolved = append(resolved, row)
			continue
		}
		kept = append(kept, row)
	}
	return kept, resolved
}

// resolveExecutions marks failed rows answered by an identical successful retry
// or by a passing verification of the same check and root at the generation the
// earlier receipts left in force. Any terminal command carries a verdict, so a
// verdict alone proves nothing. The decision reads only stamped values, so a
// later change or restart cannot bring the reminder back.
func resolveExecutions(executions []executionStateRow) (resolved []executionStateRow) {
	for i := range executions {
		row := &executions[i]
		if !row.failed {
			continue
		}
		for _, later := range executions[i+1:] {
			if resolvesExecution(*row, later) {
				row.State = reminderResolved
				resolved = append(resolved, *row)
				break
			}
		}
	}
	return resolved
}

// refreshedExecutions drops an older rejected execution when a newer rejection
// repeats the same command in the same working directory.
func refreshedExecutions(executions []executionStateRow, repeated executionStateRow) []executionStateRow {
	if repeated.Status != string(api.InvocationStatusRejected) || repeated.rawCommand == "" {
		return executions
	}
	kept := executions[:0]
	for _, row := range executions {
		if row.Status == string(api.InvocationStatusRejected) && row.Tool == repeated.Tool && row.rawCwd == repeated.rawCwd && row.rawCommand == repeated.rawCommand {
			continue
		}
		kept = append(kept, row)
	}
	return kept
}

func resolvesExecution(failed, later executionStateRow) bool {
	if later.failed || !later.completed || !later.Invoked {
		return false
	}
	if failed.ArgsDigest != "" && later.Tool == failed.Tool && later.ArgsDigest == failed.ArgsDigest {
		return true
	}
	if failed.Status == string(api.InvocationStatusRejected) && later.Tool == failed.Tool && later.rawCwd == failed.rawCwd &&
		failed.rawCommand != "" && (later.rawCommand == failed.rawCommand || strings.Contains(later.rawCommand, failed.rawCommand)) {
		return true
	}
	return failed.check && later.check && later.SourceVerdict == api.SourceVerdictPassed &&
		later.rawCommand != "" && later.rawCommand == failed.rawCommand && later.rawCwd == failed.rawCwd &&
		later.rawRevision != "" && later.rawRevision == later.priorRevision &&
		later.rawRoot != "" && (failed.rawRoot == "" || later.rawRoot == failed.rawRoot)
}

// boundedExecutionRows keeps the row cap, preferring live failures so a burst
// of later receipts cannot hide a reminder before it ages out.
func boundedExecutionRows(rows []executionStateRow) []executionStateRow {
	if len(rows) <= maxExecutionStateRows {
		return rows
	}
	keep := make([]bool, len(rows))
	kept := 0
	for _, reminders := range []bool{true, false} {
		for i := len(rows) - 1; i >= 0 && kept < maxExecutionStateRows; i-- {
			if !keep[i] && (!reminders || rows[i].failed && rows[i].State == reminderLive) {
				keep[i] = true
				kept++
			}
		}
	}
	out := make([]executionStateRow, 0, kept)
	for i, row := range rows {
		if keep[i] {
			out = append(out, row)
		}
	}
	return out
}

// isPollTool identifies calls that wait on or read back work already started.
func isPollTool(tool string) bool {
	switch tool {
	case "command_output", "terminal_read", "terminal_snapshot", "held_result", "wait":
		return true
	}
	return false
}

func executionBatchID(result *api.ToolResult) string {
	if id := strings.TrimSpace(result.AssistantMessageID); id != "" {
		return id
	}
	return result.ToolCallID
}

// agingExecutionBatches lists batch IDs oldest first with whether each advances
// age: at least one call that is not a poll, and nothing still running.
func agingExecutionBatches(messages []api.Message) (order []string, aging map[string]bool) {
	acted, running := map[string]bool{}, map[string]bool{}
	seen := map[string]bool{}
	for _, message := range messages {
		result := message.ToolResult
		if message.Role != api.MessageRoleTool || result == nil {
			continue
		}
		id := executionBatchID(result)
		if id == "" {
			continue
		}
		if !seen[id] {
			seen[id] = true
			order = append(order, id)
		}
		acted[id] = acted[id] || !isPollTool(result.Tool)
		running[id] = running[id] || result.Process != nil && result.Process.Running ||
			result.Invocation != nil && result.Invocation.Status == api.InvocationStatusRunning
	}
	aging = make(map[string]bool, len(order))
	for _, id := range order {
		aging[id] = acted[id] && !running[id]
	}
	return order, aging
}

// executionBatchAges counts, for each batch, the aging batches after it.
func executionBatchAges(messages []api.Message) map[string]int {
	order, aging := agingExecutionBatches(messages)
	ages := make(map[string]int, len(order))
	later := 0
	for i := len(order) - 1; i >= 0; i-- {
		ages[order[i]] = later
		if aging[order[i]] {
			later++
		}
	}
	return ages
}
