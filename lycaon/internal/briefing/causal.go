package briefing

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/observability"
)

// Read bounds for one action's causal evidence. Text bounds count characters,
// as SQLite substr does; even four-byte characters keep the text reads under
// 48 KiB and the reasoning tail under 64 KiB.
const (
	ProseReadChars  = 2048
	IntentReadChars = 2048
	ResultReadChars = 1024
	// ArgsReadChars bounds a call's own arguments.
	ArgsReadChars = 2048
	// ReasoningTailChars is the end of the reasoning text read for an excerpt.
	// Every trace of up to 16 KiB is read whole.
	ReasoningTailChars = 16384
	// ReasoningCeilingBytes is the largest stored reasoning SQLite extracts from.
	ReasoningCeilingBytes = 1 << 20
	reasoningRunes        = 600
)

// ActionRef anchors evidence on one tool call in one session and worker scope.
type ActionRef struct {
	SessionID   string
	WorkerJobID string
	ToolCallID  string
}

// ReasoningState explains why reasoning was or was not used.
type ReasoningState string

const (
	ReasoningAvailable   ReasoningState = "available"
	ReasoningAbsent      ReasoningState = "absent"
	ReasoningDetailsOnly ReasoningState = "details_only"
	ReasoningOversized   ReasoningState = "oversized"
	ReasoningMalformed   ReasoningState = "malformed"
)

// ReasoningRead is the bounded tail of one output's plaintext reasoning. Opaque
// details are counted, never read.
type ReasoningRead struct {
	Present     bool
	OverCeiling bool
	// TextChars is the full text length; Tail is at most ReasoningTailChars of
	// its end.
	TextChars int64
	Details   int64
	Tail      string
}

// CausalRead is the bounded transcript material around one action.
type CausalRead struct {
	Found         bool
	OutputID      string
	Prose         string
	Reasoning     ReasoningRead
	CallsInOutput int
	IntentID      string
	Intent        string
	Results       []ResultExcerpt
	// Call is the action's own name, bounded arguments and result.
	Call CallExcerpt
}

// CallExcerpt is one tool call as the transcript recorded it.
type CallExcerpt struct {
	Name   string
	Args   string
	Result *ResultExcerpt
}

// CausalReader reads settled transcript material for one action without
// loading the session.
type CausalReader interface {
	ReadActionCause(ctx context.Context, ref ActionRef) (CausalRead, error)
}

// ApplyCause fills the transcript-derived inputs of action evidence.
func ApplyCause(ev *ActionEvidence, read CausalRead) {
	if !read.Found {
		gap := "the action's model output was not found"
		ev.UserIntent.Unavailable, ev.AssistantProse.Unavailable, ev.Reasoning.Gap = gap, gap, gap
		return
	}
	ev.UserIntent = Excerpt{Text: read.Intent, Ref: messageRef(read.IntentID)}
	ev.AssistantProse = Excerpt{Text: read.Prose, Ref: messageRef(read.OutputID)}
	ev.Results = read.Results
	ev.Reasoning = ReasoningExcerpt(read.Reasoning, messageRef(read.OutputID), read.CallsInOutput > 1).Input()
}

// ReasoningResult is an excerpt and the state that produced it.
type ReasoningResult struct {
	Excerpt *Reasoning
	State   ReasoningState
}

// Input is the section input: the excerpt, or the gap for a state that had
// reasoning it could not use.
func (r ReasoningResult) Input() ReasoningInput {
	switch r.State {
	case ReasoningAvailable:
		return ReasoningInput{Excerpt: r.Excerpt}
	case ReasoningAbsent:
		return ReasoningInput{}
	default:
		return ReasoningInput{Gap: string(r.State)}
	}
}

// ReasoningExcerpt redacts the whole tail before keeping its last runes, so a
// secret crossing the cut is still redacted.
func ReasoningExcerpt(read ReasoningRead, ref string, shared bool) ReasoningResult {
	switch {
	case !read.Present:
		return ReasoningResult{State: ReasoningAbsent}
	case read.OverCeiling:
		return ReasoningResult{State: ReasoningOversized}
	}
	text := strings.TrimSpace(read.Tail)
	if text == "" {
		if read.Details > 0 {
			return ReasoningResult{State: ReasoningDetailsOnly}
		}
		return ReasoningResult{State: ReasoningAbsent}
	}
	cut := read.TextChars > int64(utf8.RuneCountInString(read.Tail))
	text = observability.RedactCaptureText(text)
	if runes := []rune(text); len(runes) > reasoningRunes {
		text, cut = string(runes[len(runes)-reasoningRunes:]), true
	}
	if !utf8.ValidString(text) {
		return ReasoningResult{State: ReasoningMalformed}
	}
	return ReasoningResult{Excerpt: &Reasoning{Text: text, Ref: ref, Shared: shared, Truncated: cut}, State: ReasoningAvailable}
}

func messageRef(id string) string {
	if id == "" {
		return ""
	}
	return "message:" + id
}
