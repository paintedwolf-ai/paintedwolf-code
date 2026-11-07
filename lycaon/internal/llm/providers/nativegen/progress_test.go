package nativegen

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestProgressTracksBoundedPrefillAndOneTokenDecode(t *testing.T) {
	cursor := &progressCursor{requestID: 9, prompt: 6, maximum: 2, chunk: 4}
	first := &progressResult{expected: cursor}
	first.Progress.RequestID = 9
	first.Progress.ProcessedPromptTokens = 4
	testutil.FailErr(t, "validate prefill progress", first.validate())
	token := uint32(12)
	second := &progressResult{expected: cursor, Text: "answer"}
	second.Progress.RequestID = 9
	second.Progress.ProcessedPromptTokens = 6
	second.Progress.GeneratedTokens = 1
	second.Progress.Token = &token
	testutil.FailErr(t, "validate decode progress", second.validate())
	stop := "eos"
	last := &progressResult{expected: cursor}
	last.Progress.RequestID = 9
	last.Progress.ProcessedPromptTokens = 6
	last.Progress.GeneratedTokens = 2
	last.Progress.StopReason = &stop
	if err := last.validate(); err != nil || !cursor.terminal {
		t.Fatalf("terminal step = %v, %+v", err, cursor)
	}
	if err := last.validate(); err == nil {
		t.Fatal("accepted a repeated terminal step")
	}
}

func TestProgressRequiresTheRequestedTerminationContract(t *testing.T) {
	for _, tc := range []struct {
		stop     string
		schema   bool
		accepted bool
	}{
		{"structured_output", false, false},
		{"structured_output", true, true},
		{"stop_token", false, false},
	} {
		t.Run(tc.stop+"_schema_"+map[bool]string{false: "absent", true: "present"}[tc.schema], func(t *testing.T) {
			cursor := &progressCursor{requestID: 1, prompt: 2, processed: 2, maximum: 4, chunk: 2, schema: tc.schema}
			result := &progressResult{expected: cursor}
			result.Progress.RequestID = 1
			result.Progress.ProcessedPromptTokens = 2
			result.Progress.GeneratedTokens = 1
			result.Progress.StopReason = &tc.stop
			if tc.stop == "structured_output" {
				token := uint32(12)
				result.Progress.Token = &token
			}
			if err := result.validate(); (err == nil) != tc.accepted {
				t.Fatalf("termination acceptance = %v, want %v: %v", err == nil, tc.accepted, err)
			}
			if cursor.terminal != tc.accepted {
				t.Fatalf("terminal cursor = %v, want %v", cursor.terminal, tc.accepted)
			}
		})
	}
}

func TestProgressRejectsStaleUnboundedAndPrematureOutput(t *testing.T) {
	for _, mutate := range []func(*progressResult){
		func(r *progressResult) { r.Progress.RequestID = 10 },
		func(r *progressResult) { r.Progress.ProcessedPromptTokens = 5 },
		func(r *progressResult) { r.Progress.ProcessedPromptTokens = -1 },
		func(r *progressResult) { r.Progress.GeneratedTokens = 1 },
		func(r *progressResult) { r.Progress.ReusedPrefixTokens = 1 },
		func(r *progressResult) { r.Text = "premature" },
		func(r *progressResult) { token := uint32(12); r.Progress.Token = &token },
		func(r *progressResult) { stop := "eos"; r.Progress.StopReason = &stop },
	} {
		cursor := &progressCursor{requestID: 9, prompt: 6, maximum: 2, chunk: 4}
		result := &progressResult{expected: cursor}
		result.Progress.RequestID = 9
		result.Progress.ProcessedPromptTokens = 4
		mutate(result)
		if err := result.validate(); err == nil {
			t.Fatalf("accepted inconsistent progress: %+v", result.Progress)
		}
		if cursor.processed != 0 || cursor.generated != 0 || cursor.terminal {
			t.Fatal("rejected progress changed the cursor")
		}
	}
}

func TestProgressCancellationDoesNotAdvancePhysicalWork(t *testing.T) {
	cursor := &progressCursor{requestID: 2, prompt: 9, processed: 4, reused: 4, maximum: 3, chunk: 4, cancel: true}
	result := &progressResult{expected: cursor}
	result.Progress.RequestID = 2
	result.Progress.ProcessedPromptTokens = 4
	result.Progress.ReusedPrefixTokens = 4
	stop := "canceled"
	result.Progress.StopReason = &stop
	if err := result.validate(); err != nil || !cursor.terminal {
		t.Fatalf("canceled progress = %v, %+v", err, cursor)
	}
}
