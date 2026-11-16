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

// presenter describes one family's settled results. A family that has no
// presenter for a tool leaves the tool on its existing card.
type presenter func(result api.ToolResult) Parts

var presenters = map[string]presenter{}

func register(p presenter, tools ...string) {
	for _, tool := range tools {
		presenters[tool] = p
	}
}

// Tools lists the tools that have a presenter.
func Tools() []string {
	out := make([]string, 0, len(presenters))
	for tool := range presenters {
		out = append(out, tool)
	}
	return out
}

// ForCall describes a call before its result arrives. Command calls are
// described by their activities instead.
func ForCall(call api.ToolCall) *api.ActionDescriptor {
	if _, ok := presenters[call.Name]; !ok || isCommandTool(call.Name) {
		return nil
	}
	label := toolpresentation.Label(call.Name)
	if label == "" {
		return nil
	}
	ran := strings.TrimSpace(call.DisplayTitle)
	if ran == "" {
		ran = toolpresentation.Title(call.Name, call.Args)
	}
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
	describe, ok := presenters[result.Tool]
	if !ok || (result.CommandProgram != nil && len(result.CommandProgram.Activities) > 0) {
		return nil
	}
	label := toolpresentation.Label(result.Tool)
	if label == "" {
		return nil
	}
	parts := describe(result)
	ran := parts.Ran
	if ran == "" {
		ran = resultTarget(result)
	}
	noun := parts.Noun
	if noun == "" {
		noun = nounFor(result.Tool)
	}
	ranText := api.ActionRan{Text: ran, Noun: noun}
	if full, ok := submittedCommand(result); ok && full == ran && len(commandMarks) > 0 {
		ranText.Redaction = &api.HostSecretRedactionMeta{Spans: retarget(commandMarks, RanTextField)}
	}
	return &api.ActionDescriptor{
		Label:   label,
		Status:  descriptorStatus(result),
		Ran:     ranText,
		Facts:   ordered(parts.Facts),
		Row:     nonNil(parts.Row),
		Preview: parts.Preview,
		Links:   resultLinks(result),
		Opener:  parts.Opener,
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

func nonNil[T any](items []T) []T {
	if items == nil {
		return []T{}
	}
	return items
}
