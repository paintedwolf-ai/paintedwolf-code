// Package actioncard describes each visible action for Den's one action card.
// Descriptors are a pure function of the call, its result and its command
// activity: they read typed facts only, never output text, and are composed
// on read rather than stored.
package actioncard

import (
	"strings"

	"github.com/lycaon/lycaon/internal/toolpresentation"
	"github.com/lycaon/lycaon/pkg/api"
)

// Parts is what a tool family adds to the shared descriptor.
type Parts struct {
	Noun    string
	Ran     string
	Facts   []api.ActionFact
	Row     []api.ActionFactKind
	Preview *api.ActionPreview
	Opener  *api.ActionOpener
	FoldKey string
}

// presenter adds one family's facts, preview, opener and fold to the facts
// its handler stated and the facts every result carries in typed fields.
type presenter func(result api.ToolResult) Parts

var presenters = map[string]presenter{}

func register(p presenter, tools ...string) {
	for _, tool := range tools {
		presenters[tool] = p
	}
}

// Tools lists the tools that have a family presenter.
func Tools() []string {
	out := make([]string, 0, len(presenters))
	for tool := range presenters {
		out = append(out, tool)
	}
	return out
}

// mcpToolPrefix starts every MCP provider tool's qualified name.
const mcpToolPrefix = "mcp_"

// labelFor is the card's name. Worker tools have none and keep their own card.
func labelFor(tool string) string {
	if label := toolpresentation.Label(tool); label != "" {
		return label
	}
	if strings.HasPrefix(tool, mcpToolPrefix) {
		return "Use MCP tool"
	}
	return ""
}

// ForCall describes a call before its result arrives. Command calls are
// described by their activities instead.
func ForCall(call api.ToolCall) *api.ActionDescriptor {
	label := labelFor(call.Name)
	if label == "" || isCommandTool(call.Name) {
		return nil
	}
	ran := firstNonEmpty(strings.TrimSpace(call.DisplayTitle), toolpresentation.Title(call.Name, call.Args), call.Name)
	return &api.ActionDescriptor{
		Label:  label,
		Status: statusRunning,
		Ran:    api.ActionRan{Text: ran, Noun: nounFor(call.Name)},
		Facts:  []api.ActionFact{},
		Row:    []api.ActionFactKind{},
		Links:  []api.ActionLink{inputLink(call.ArgsRef)},
	}
}

// ForResult describes a settled call whose card is not one of its program's
// activities: a call with no program, or a program that recorded none.
// commandMarks are the redaction marks on its command argument, if any.
func ForResult(result api.ToolResult, commandMarks []api.RedactedSpan) *api.ActionDescriptor {
	label := labelFor(result.Tool)
	if label == "" || (result.CommandProgram != nil && len(result.CommandProgram.Activities) > 0) {
		return nil
	}
	var parts Parts
	if describe, ok := presenters[result.Tool]; ok {
		parts = describe(result)
	}
	facts := append(append(parts.Facts, result.CardFacts...), typedFacts(result)...)
	facts = append(facts, statusFacts(result, facts)...)
	if took, ok := invocationTook(result.Invocation); ok {
		facts = append(facts, took)
	}
	facts = ordered(facts)
	row := shownRow(parts.Row, facts)
	if len(row) == 0 {
		row = defaultRow(facts)
	}
	opener := parts.Opener
	if opener == nil {
		opener = fileOpener(result.FileEditPreviews)
	}
	ran := firstNonEmpty(parts.Ran, resultTarget(result), result.Tool)
	noun := firstNonEmpty(parts.Noun, nounFor(result.Tool))
	ranText := api.ActionRan{Text: ran, Noun: noun}
	if full, ok := submittedCommand(result); ok && full == ran && len(commandMarks) > 0 {
		ranText.Redaction = &api.HostSecretRedactionMeta{Spans: retarget(commandMarks, RanTextField)}
	}
	return &api.ActionDescriptor{
		Label:   label,
		Status:  descriptorStatus(result),
		Ran:     ranText,
		Facts:   facts,
		Row:     row,
		Preview: parts.Preview,
		Links:   resultLinks(result),
		Opener:  opener,
		FoldKey: parts.FoldKey,
	}
}

const (
	statusRunning = "running"
	statusDone    = "done"
	statusError   = "error"
)

// descriptorStatus follows a program's own state when the result carries one.
func descriptorStatus(result api.ToolResult) string {
	if program := result.CommandProgram; program != nil {
		return activityStatus(program.State)
	}
	return resultStatus(result)
}

func resultStatus(result api.ToolResult) string {
	if result.Outcome == api.ToolResultOutcomeRejected || result.Outcome == api.ToolResultOutcomeError {
		return statusError
	}
	if invocation := result.Invocation; invocation != nil {
		switch invocation.Status {
		case api.InvocationStatusRunning:
			return statusRunning
		case api.InvocationStatusRejected, api.InvocationStatusError, api.InvocationStatusInterrupted:
			return statusError
		case api.InvocationStatusCompleted:
		}
	}
	if result.Process != nil && result.Process.Running {
		return statusRunning
	}
	return statusDone
}

// resultTarget is the host's screened readable target for a result.
func resultTarget(result api.ToolResult) string {
	if subject := strings.TrimSpace(result.DisplaySubject); subject != "" {
		return subject
	}
	if title := strings.TrimSpace(result.DisplayTitle); title != "" {
		return title
	}
	return toolpresentation.Title(result.Tool, result.ToolArgs)
}

func inputLink(ref *api.ChatContentReference) api.ActionLink {
	return api.ActionLink{Kind: linkInput, ContentRef: ref}
}

const (
	linkInput   = "input"
	linkOutput  = "output"
	linkReceipt = "receipt"
)

func resultLinks(result api.ToolResult) []api.ActionLink {
	links := []api.ActionLink{inputLink(result.ToolArgsRef)}
	switch {
	case result.Process != nil && result.Process.Handle != "":
		links = append(links, api.ActionLink{Kind: linkOutput, ProcessHandle: result.Process.Handle})
	case result.ContentRef != nil || strings.TrimSpace(result.Content) != "":
		links = append(links, api.ActionLink{Kind: linkOutput, ContentRef: result.ContentRef})
	}
	if program := result.CommandProgram; program != nil {
		links = append(links, api.ActionLink{Kind: linkReceipt, Receipts: []api.ActionReceiptScope{{ProgramID: program.ID}}})
	}
	return links
}
