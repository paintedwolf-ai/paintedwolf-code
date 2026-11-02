package responserecovery

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/failure"
)

func TestEligibilityUsesFailureAndCommitFacts(t *testing.T) {
	malformed := &failure.ProviderOutputProtocolError{Kind: failure.MalformedEnvelope, ProviderID: "p", Model: "m"}
	dropped := &failure.ProviderToolCallUnparsedError{ProviderID: "p", Model: "m", Diagnostic: "unexpected_end"}
	for _, tc := range []struct {
		name   string
		change func(*Facts)
		code   string
	}{
		{"text without tools", func(*Facts) {}, "MODEL_RESPONSE_INVALID"},
		{"schema unchanged", func(f *Facts) { f.StructuredOutput = true }, "MODEL_RESPONSE_INVALID"},
		{"unknown commit", func(f *Facts) { f.Commit = CommitUnknown }, ""},
		{"committed", func(f *Facts) { f.Commit = ResponseCommitted }, ""},
		{"canceled", func(f *Facts) { f.Canceled = true }, ""},
		{"no remaining call", func(f *Facts) { f.RemainingCalls = 0 }, ""},
		{"decode disagreement", func(f *Facts) { f.Cause = &failure.ProviderOutputProtocolError{Kind: failure.DecodeMismatch} }, ""},
		{"output bound", func(f *Facts) { f.Cause = &failure.ProviderOutputProtocolError{Kind: failure.OutputBound} }, ""},
		{"unknown error", func(f *Facts) { f.Cause = errors.New("unexpected control") }, ""},
		{"dropped native call", func(f *Facts) { f.Cause = dropped; f.NativeTools = true }, "TOOL_CALL_UNPARSED"},
		{"dropped without tools", func(f *Facts) { f.Cause = dropped }, ""},
		{"dropped schema", func(f *Facts) { f.Cause = dropped; f.NativeTools = true; f.StructuredOutput = true }, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := Facts{Cause: malformed, Commit: ResponseUncommitted, RemainingCalls: 1}
			tc.change(&f)
			var state State
			if got := state.Decide(f); got.Code != tc.code {
				t.Fatalf("decision=%+v want %q", got, tc.code)
			}
		})
	}
}

func TestAlternatingFailuresShareAllowanceUntilAccepted(t *testing.T) {
	var state State
	f := Facts{Cause: &failure.ProviderOutputProtocolError{Kind: failure.MalformedEnvelope}, Commit: ResponseUncommitted, RemainingCalls: 9, NativeTools: true}
	for i := 0; i < MaxConsecutive; i++ {
		if state.Decide(f).Code == "" {
			t.Fatal("replacement refused before bound")
		}
		state.Replaced()
		f.Cause = &failure.ProviderToolCallUnparsedError{}
	}
	if state.Consecutive() != MaxConsecutive || state.Decide(f).Code != "" {
		t.Fatal("alternating failures replenished allowance")
	}
	state.Accepted()
	if state.Consecutive() != 0 || state.Decide(f).Code != "TOOL_CALL_UNPARSED" {
		t.Fatal("accepted response did not reset allowance")
	}
}
