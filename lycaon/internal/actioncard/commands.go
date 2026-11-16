package actioncard

import (
	"cmp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/toolpresentation"
	"github.com/lycaon/lycaon/internal/toolreach"
	"github.com/lycaon/lycaon/pkg/api"
)

// Tools that run as command programs; their cards are their activities.
var commandTools = map[string]bool{"command": true, "verify": true, "summarize": true, "survey_repo": true, "code_rewrite": true}

func isCommandTool(tool string) bool { return commandTools[tool] }

func init() {
	register(commandResult, "command", "verify", "command_output", "command_stop")
}

// commandResult describes a command call with no program, such as one the
// host refused before parsing, and the output and stop calls on a job.
func commandResult(result api.ToolResult) Parts {
	parts := Parts{Noun: "command"}
	if command, ok := submittedCommand(result); ok {
		parts.Ran = command
	}
	switch resultStatus(result) {
	case statusError:
		parts.Facts = append(parts.Facts, resultFact(rejectedOutcome(result)))
		if code := firstCode(result); code != "" {
			parts.Facts = append(parts.Facts, stoppedByFact("host_stop", code))
		}
		parts.Row = []api.ActionFactKind{api.ActionFactResult}
	case statusDone:
		parts.Facts = append(parts.Facts, resultFact("completed"))
		parts.Row = []api.ActionFactKind{api.ActionFactResult}
	}
	if took, ok := invocationTook(result.Invocation); ok {
		parts.Facts = append(parts.Facts, took)
	}
	if result.Process != nil && result.Process.Handle != "" {
		parts.Opener = &api.ActionOpener{Kind: "running", ProcessHandle: result.Process.Handle}
	}
	return parts
}

// ForActivity describes one activity of a command program.
// commandMarks are the redaction marks on the submitted command argument.
func ForActivity(program api.CommandProgramReceipt, activity api.CommandActivity, result api.ToolResult, commandMarks []api.RedactedSpan) api.ActionDescriptor {
	label := strings.TrimSpace(activity.Label)
	if label == "" {
		label = toolpresentation.Label(result.Tool)
	}
	descriptor := api.ActionDescriptor{
		Label:  label,
		Status: activityStatus(activity.State),
		Ran:    commandRan(activityCommand(program, activity, result, len(commandMarks) > 0), result, commandMarks),
		Row:    []api.ActionFactKind{},
		Links:  activityLinks(program, activity, result),
	}
	var facts []api.ActionFact
	switch activity.State {
	case "completed":
		facts = append(facts, exitFact(exitOutcome(activity.StatusCode), activity.StatusCode))
		descriptor.Row = append(descriptor.Row, api.ActionFactResult)
	case "failed":
		facts = append(facts, exitFact("failed", activity.StatusCode))
		descriptor.Row = append(descriptor.Row, api.ActionFactResult)
	case "refused":
		facts = append(facts, resultFact("refused"))
		if code := firstNonEmpty(program.StopCode, firstCode(result)); code != "" {
			facts = append(facts, stoppedByFact("host_stop", code))
		}
		descriptor.Row = append(descriptor.Row, api.ActionFactResult)
	case "interrupted":
		facts = append(facts, resultFact("interrupted"))
		descriptor.Row = append(descriptor.Row, api.ActionFactResult)
	}
	// A program's timing is the activity's only when the activity is the whole program.
	if len(program.Activities) == 1 {
		if took, ok := tookFact(program.StartedAt, program.SettledAt); ok {
			facts = append(facts, took)
			descriptor.Row = append(descriptor.Row, api.ActionFactTook)
		}
	}
	if activity.ExecutionCount > 1 {
		facts = append(facts, stepsFact(activity.ExecutionCount, activity.FailedCount, "executions"))
	}
	if activity.JobHandle != "" {
		state := "finished"
		if activity.State == "running" || activity.State == "waiting" {
			state = "running"
		}
		facts = append(facts, processFact(state))
		descriptor.Opener = &api.ActionOpener{Kind: "running", ProcessHandle: activity.JobHandle}
	}
	descriptor.Facts = ordered(facts)
	if len(activity.ExecutionIDs) > 0 {
		descriptor.Preview = &api.ActionPreview{Kind: "output", Receipts: []api.ActionReceiptScope{{ProgramID: program.ID, ActivityID: activity.ID}}}
	}
	return descriptor
}

// commandRan carries the command's redaction marks when it shows the whole
// submitted command, re-addressed to the Ran text.
func commandRan(text string, result api.ToolResult, marks []api.RedactedSpan) api.ActionRan {
	ran := api.ActionRan{Text: text, Noun: "command"}
	if full, ok := submittedCommand(result); ok && full == text && len(marks) > 0 {
		ran.Redaction = &api.HostSecretRedactionMeta{Spans: retarget(marks, RanTextField)}
	}
	return ran
}

// RanTextField addresses redaction marks within a descriptor's Ran text.
const RanTextField = "ran.text"

func retarget(spans []api.RedactedSpan, field string) []api.RedactedSpan {
	out := make([]api.RedactedSpan, len(spans))
	for i, span := range spans {
		span.Field = field
		out[i] = span
	}
	return out
}

func activityStatus(state string) string {
	switch state {
	case "running", "waiting":
		return statusRunning
	case "failed", "refused", "interrupted":
		return statusError
	}
	return statusDone
}

func exitOutcome(code *int) string {
	if code != nil && *code != 0 {
		return "failed"
	}
	return "passed"
}

func rejectedOutcome(result api.ToolResult) string {
	if result.Outcome == api.ToolResultOutcomeRejected {
		return "refused"
	}
	return "failed"
}

func firstCode(result api.ToolResult) string {
	if len(result.Codes) > 0 {
		return result.Codes[0]
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// submittedCommand is the command a call ran: its command argument, or the
// program the host writes for a form tool.
func submittedCommand(result api.ToolResult) (string, bool) {
	if command, ok := result.ToolArgs["command"].(string); ok && strings.TrimSpace(command) != "" {
		return command, true
	}
	return toolreach.FormCommand(result.Tool, result.ToolArgs)
}

// activityCommand is the text an activity ran. An activity of a compound
// command shows only its own statements, cut from the submitted source by
// the activity's byte spans.
func activityCommand(program api.CommandProgramReceipt, activity api.CommandActivity, result api.ToolResult, argsRedacted bool) string {
	full, ok := submittedCommand(result)
	if !ok {
		return activity.Subject
	}
	if len(program.Activities) <= 1 {
		return full
	}
	// A replacement mark changes the text's length, so byte spans no longer line up.
	if argsRedacted {
		return activity.Subject
	}
	if slice := sliceSource(full, activity.Spans, program.SourceRef); slice != "" {
		return slice
	}
	return activity.Subject
}

// sliceSource joins the source text at spans on one source unit, merging
// overlapping spans and dropping repeats. Offsets are UTF-8 bytes.
func sliceSource(source string, spans []api.CommandActivitySpan, sourceRef string) string {
	type byteRange struct{ start, end int }
	var ranges []byteRange
	for _, span := range spans {
		if span.SourceRef != sourceRef || span.StartByte < 0 || span.StartByte >= span.EndByte || span.EndByte > len(source) {
			continue
		}
		ranges = append(ranges, byteRange{span.StartByte, span.EndByte})
	}
	slices.SortFunc(ranges, func(a, b byteRange) int { return cmp.Or(cmp.Compare(a.start, b.start), cmp.Compare(a.end, b.end)) })
	var merged []byteRange
	for _, r := range ranges {
		if last := len(merged) - 1; last >= 0 && r.start <= merged[last].end {
			merged[last].end = max(merged[last].end, r.end)
			continue
		}
		merged = append(merged, r)
	}
	var parts []string
	for _, r := range merged {
		text := strings.Trim(source[r.start:r.end], "\n")
		if !utf8.ValidString(text) || text == "" || (len(parts) > 0 && parts[len(parts)-1] == text) {
			continue
		}
		parts = append(parts, text)
	}
	return strings.Join(parts, "\n")
}

func activityLinks(program api.CommandProgramReceipt, activity api.CommandActivity, result api.ToolResult) []api.ActionLink {
	links := []api.ActionLink{inputLink(result.ToolArgsRef)}
	switch {
	case activity.JobHandle != "":
		links = append(links, api.ActionLink{Kind: linkOutput, ProcessHandle: activity.JobHandle})
	case result.ContentRef != nil || strings.TrimSpace(result.Content) != "":
		links = append(links, api.ActionLink{Kind: linkOutput, ContentRef: result.ContentRef})
	}
	if len(activity.ExecutionIDs) > 0 {
		links = append(links, api.ActionLink{Kind: linkReceipt, Receipts: []api.ActionReceiptScope{{ProgramID: program.ID, ActivityID: activity.ID}}})
	}
	return links
}
