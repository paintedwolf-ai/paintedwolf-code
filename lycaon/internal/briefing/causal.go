package briefing

import (
	"context"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/observability"
)

// Read bounds for one action's causal evidence. Text bounds count characters,
// as SQLite substr does; even four-byte characters keep a read under 48 KiB.
const (
	ProseReadChars     = 2048
	IntentReadChars    = 2048
	ResultReadChars    = 1024
	ReasoningReadBytes = 16 << 10
	reasoningRunes     = 600
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

// CausalRead is the bounded transcript material around one action.
type CausalRead struct {
	Found          bool
	OutputID       string
	Prose          string
	ReasoningJSON  string
	ReasoningBytes int64
	CallsInOutput  int
	IntentID       string
	Intent         string
	Results        []ResultExcerpt
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
		ev.UserIntent.Unavailable, ev.AssistantProse.Unavailable, ev.ReasoningGap = gap, gap, gap
		return
	}
	ev.UserIntent = Excerpt{Text: read.Intent, Ref: messageRef(read.IntentID)}
	ev.AssistantProse = Excerpt{Text: read.Prose, Ref: messageRef(read.OutputID)}
	ev.RecentResults = read.Results
	reasoning, state := ReasoningExcerpt(read)
	if state == ReasoningAvailable {
		ev.Reasoning = reasoning
	} else if state != ReasoningAbsent {
		ev.ReasoningGap = string(state)
	}
}

// ReasoningExcerpt redacts the whole plaintext before keeping its suffix, so a
// secret crossing the cut is still redacted. Opaque details are never read.
func ReasoningExcerpt(read CausalRead) (*Reasoning, ReasoningState) {
	if read.ReasoningBytes == 0 {
		return nil, ReasoningAbsent
	}
	if read.ReasoningJSON == "" {
		return nil, ReasoningOversized
	}
	var payload struct {
		Text    string            `json:"text"`
		Details []json.RawMessage `json:"details"`
	}
	if err := json.Unmarshal([]byte(read.ReasoningJSON), &payload); err != nil {
		return nil, ReasoningMalformed
	}
	text := strings.TrimSpace(payload.Text)
	if text == "" {
		if len(payload.Details) > 0 {
			return nil, ReasoningDetailsOnly
		}
		return nil, ReasoningAbsent
	}
	text = observability.RedactCaptureText(text)
	truncated := false
	if runes := []rune(text); len(runes) > reasoningRunes {
		text, truncated = string(runes[len(runes)-reasoningRunes:]), true
	}
	if !utf8.ValidString(text) {
		return nil, ReasoningMalformed
	}
	return &Reasoning{Text: text, Ref: messageRef(read.OutputID), Shared: read.CallsInOutput > 1, Truncated: truncated}, ReasoningAvailable
}

func messageRef(id string) string {
	if id == "" {
		return ""
	}
	return "message:" + id
}
