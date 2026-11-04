package guard

import (
	"strings"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/internal/limits"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/pkg/api"
)

const (
	WaitHiddenTextCode = "WAIT_HIDDEN_TEXT"
	// waitToolName parks a coordinator turn until its conditions change.
	waitToolName = "wait"
	// waitHandleSampleSize bounds the handles a refused wait names.
	waitHandleSampleSize = 6
)

// WaitVisibility is the publication debt durable history holds in the current
// intent window. Any later note settles earlier unpublished text: it is a new
// progress update, and the model need not repeat every earlier deduction.
type WaitVisibility struct {
	// Unpublished is text on a carrier with no later note.
	Unpublished bool
	// RefusedWaits counts refused waits since the latest note.
	RefusedWaits int
	// CitationAttempted describes the latest unpublished text.
	CitationAttempted bool
	// FallbackSettled means a wait already went ahead past this debt. Only newer
	// unpublished text is new debt; an empty step does not reopen it.
	FallbackSettled bool
}

// Exhausted reports that refusing another wait would only repeat the refusal.
func (v WaitVisibility) Exhausted() bool {
	return v.Unpublished && v.RefusedWaits >= limits.DefaultWaitVisibilityRepairs
}

// NeedsFallback reports exhausted debt the host has not yet settled for a wait.
func (v WaitVisibility) NeedsFallback() bool { return v.Exhausted() && !v.FallbackSettled }

// Pending reports debt the wait guard still enforces.
func (v WaitVisibility) Pending() bool { return v.Unpublished && !v.Exhausted() }

// DeriveWaitVisibility reads carrier and note rows. A crash between the two
// leaves the text unpublished, which is true: the user did not receive it.
func DeriveWaitVisibility(history []api.Message) WaitVisibility {
	var out WaitVisibility
	for _, msg := range history[api.UserIntentBoundary(history):] {
		switch {
		case api.IsAgentNoteMessage(msg):
			out = WaitVisibility{}
		case msg.Role == api.MessageRoleAssistant:
			if len(msg.ToolCalls) == 0 || !api.IsPromptHistoryMessage(msg) {
				continue
			}
			text := hostmarker.StripParrotedTransportMarkers(msg.Content)
			if guidance.MidTurnTextHasSpeech(text) {
				out.Unpublished = true
				out.CitationAttempted = guidance.MidTurnTextAttemptsCitation(text)
				out.FallbackSettled = false
			}
		case msg.Role == api.MessageRoleTool && msg.ToolResult != nil && msg.ToolResult.Tool == waitToolName:
			if refusedForVisibility(msg) {
				out.RefusedWaits++
			} else if out.Exhausted() {
				out.FallbackSettled = true
			}
		}
	}
	return out
}

func refusedForVisibility(msg api.Message) bool {
	for _, code := range msg.ToolResult.Codes {
		if code == WaitHiddenTextCode {
			return true
		}
	}
	return false
}

// ObserveWaitVisibility publishes the facts WAIT_HIDDEN_TEXT reads. The current
// response's note commits before any tool runs, so it already counts here.
func ObserveWaitVisibility(sess *api.Session, history []api.Message, tool string, publishes bool, gc *oar.GuardContext) {
	if gc == nil || sess == nil || sess.IsWorkerChild() || tool != waitToolName {
		return
	}
	gc.Invocation.Tool = tool
	observeToolTurnText(history, tool, gc)
	if !publishes {
		return
	}
	visibility := DeriveWaitVisibility(history)
	gc.Session.UnpublishedToolTextPending = visibility.Pending()
	gc.Session.MidTurnCitationAttempted = visibility.CitationAttempted
	gc.Session.MidTurnCitationsRequired = guidance.IntentWindowHasCitableObservations(history)
	if visibility.Pending() {
		gc.PutRejectData(WaitHiddenTextCode, map[string]any{
			"observed_handles_sample": strings.Join(guidance.IntentWindowHandles(history, waitHandleSampleSize), ", "),
		})
	}
}

// observeToolTurnText describes the batch holding this call: whether it has text
// after marker stripping, and whether a note follows it.
func observeToolTurnText(history []api.Message, tool string, gc *oar.GuardContext) {
	noteSeen := false
	for i := len(history) - 1; i >= 0; i-- {
		msg := history[i]
		if api.IsUserIntentMessage(msg) {
			return
		}
		if api.IsAgentNoteMessage(msg) {
			noteSeen = true
			continue
		}
		if msg.Role != api.MessageRoleAssistant {
			continue
		}
		for _, call := range msg.ToolCalls {
			if call.Name == tool {
				gc.Session.ToolTurnHasText = strings.TrimSpace(hostmarker.StripParrotedTransportMarkers(msg.Content)) != ""
				gc.Session.ToolTurnHasAgentNote = noteSeen
				return
			}
		}
		return
	}
}
