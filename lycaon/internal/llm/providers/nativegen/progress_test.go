package nativegen

import "testing"

func TestProgressTracksBoundedPrefillAndOneTokenDecode(t *testing.T) {
	cursor := &progressCursor{requestID: 9, prompt: 6, maximum: 2, chunk: 4}
	first := &progressResult{expected: cursor}
	first.Progress.RequestID = 9
	first.Progress.ProcessedPromptTokens = 4
	if err := first.validate(); err != nil {
		t.Fatal(err)
	}
	token := uint32(12)
	second := &progressResult{expected: cursor, Text: "answer"}
	second.Progress.RequestID = 9
	second.Progress.ProcessedPromptTokens = 6
	second.Progress.GeneratedTokens = 1
	second.Progress.Token = &token
	if err := second.validate(); err != nil {
		t.Fatal(err)
	}
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
