package briefingstore

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/briefing"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// The store returns at most a tail of the reasoning text, whole wherever the
// text fits the window, and nothing above the ceiling.
func TestReasoningReadIsBoundedAndWholeWhereItFits(t *testing.T) {
	database := testdbfixture.Open(t, "briefing-reasoning-tail.db")
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, t.TempDir())
	sessions := store.NewSQL(database)
	session, err := sessions.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	jsonOverhead := len(`{"text":""}`)
	cases := []struct {
		name string
		text string
		// whole is true when the tail must equal the stored text.
		whole, overCeiling bool
	}{
		{name: "short", text: strings.Repeat("a", 1000), whole: true},
		{name: "16 KiB of JSON", text: strings.Repeat("a", 16<<10-jsonOverhead), whole: true},
		{name: "window", text: strings.Repeat("a", briefing.ReasoningTailChars), whole: true},
		{name: "past the window", text: "head " + strings.Repeat("b", briefing.ReasoningTailChars)},
		{name: "four-byte characters", text: strings.Repeat("😀", briefing.ReasoningTailChars+100)},
		{name: "at the ceiling", text: strings.Repeat("c", briefing.ReasoningCeilingBytes-jsonOverhead)},
		{name: "above the ceiling", text: strings.Repeat("d", briefing.ReasoningCeilingBytes), overCeiling: true},
	}
	var history []api.Message
	for _, tc := range cases {
		history = append(history, api.Message{ID: "m-" + tc.name, Role: api.MessageRoleAssistant, ToolCalls: call(tc.name), ModelReasoning: &api.ModelReasoning{Text: tc.text}})
	}
	history = append(history,
		api.Message{ID: "m-null", Role: api.MessageRoleAssistant, ToolCalls: call("null")},
		api.Message{ID: "m-empty", Role: api.MessageRoleAssistant, ToolCalls: call("empty"), ModelReasoning: &api.ModelReasoning{Text: "set empty below"}},
	)
	testutil.FailErr(t, "append history", sessions.AppendMessages(t.Context(), session.ID, history...))
	_, err = database.ExecContext(t.Context(), `UPDATE messages SET reasoning_json = '' WHERE id = 'm-empty'`)
	testutil.FailErr(t, "store empty reasoning", err)

	reader := NewCausal(database)
	read := func(callID string) briefing.ReasoningRead {
		t.Helper()
		got, err := reader.ReadActionCause(t.Context(), briefing.ActionRef{SessionID: session.ID, ToolCallID: callID})
		testutil.FailErr(t, "read "+callID, err)
		if !got.Found {
			t.Fatalf("%s: output not found", callID)
		}
		return got.Reasoning
	}
	for _, tc := range cases {
		got := read(tc.name)
		chars := utf8.RuneCountInString(got.Tail)
		switch {
		case tc.overCeiling:
			if !got.Present || !got.OverCeiling || got.Tail != "" || got.TextChars != 0 {
				t.Fatalf("%s: %+v", tc.name, got)
			}
		case chars > briefing.ReasoningTailChars || len(got.Tail) > 4*briefing.ReasoningTailChars || got.OverCeiling:
			t.Fatalf("%s: returned %d characters, %d bytes", tc.name, chars, len(got.Tail))
		case got.TextChars != int64(utf8.RuneCountInString(tc.text)) || !strings.HasSuffix(tc.text, got.Tail):
			t.Fatalf("%s: tail is not the end of the text (%d of %d characters)", tc.name, chars, got.TextChars)
		case tc.whole && got.Tail != tc.text:
			t.Fatalf("%s: text that fits the window was not read whole", tc.name)
		case !tc.whole && chars != briefing.ReasoningTailChars:
			t.Fatalf("%s: tail is %d characters", tc.name, chars)
		}
	}
	for _, callID := range []string{"null", "empty"} {
		got := read(callID)
		if got.Present || briefing.ReasoningExcerpt(got, "", false).State != briefing.ReasoningAbsent {
			t.Fatalf("%s reasoning: %+v", callID, got)
		}
	}
}
