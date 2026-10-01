package gotreesitter

import "testing"

func TestRecoveryRuntimeTelemetryKeepsSelectedAttemptRecord(t *testing.T) {
	EnableRecoveryRuntimeTelemetry(true)
	t.Cleanup(func() { EnableRecoveryRuntimeTelemetry(false) })

	parser := &Parser{}
	initial := &Tree{root: &Node{endByte: 4}}
	losingRetry := &Tree{root: &Node{endByte: 4}}

	parser.beginRecoveryRuntimeTelemetry()
	parser.recordRecoveryEntry()
	parser.finishRecoveryRuntimeTelemetry(initial, nil)
	parser.recordRecoveryRuntimeRetryTree(initial, "initial")

	parser.fullParseRetryPassesTaken = 1
	parser.beginRecoveryRuntimeTelemetry()
	parser.recordRecoveryEntry()
	parser.recordRecoveryEntry()
	parser.finishRecoveryRuntimeTelemetry(losingRetry, nil)
	parser.recordRecoveryRuntimeRetryTree(losingRetry, "final_merge")

	parser.finishRecoveryRuntimeRetryTelemetry(initial, 4)
	stats := parser.DebugRecoveryRuntimeStats()
	if stats.RecoveryEntryCount != 1 {
		t.Fatalf("selected attempt recovery entries = %d, want 1", stats.RecoveryEntryCount)
	}
	if stats.RetrySelectedAttempt != "initial" {
		t.Fatalf("selected attempt = %q, want initial", stats.RetrySelectedAttempt)
	}
	if stats.RetryAttemptCount != 1 || stats.RetryPassCount != 1 {
		t.Fatalf("retry counts = attempts:%d passes:%d, want 1/1", stats.RetryAttemptCount, stats.RetryPassCount)
	}

	// A scanner can request a second retry ladder with the first ladder's
	// selected tree as its incumbent. The new ladder must not replace its
	// attempt-local snapshot with zero values.
	parser.clearRecoveryRuntimeRetryTrees()
	parser.recordRecoveryRuntimeRetryTree(initial, "initial")
	parser.finishRecoveryRuntimeRetryTelemetry(initial, 4)
	stats = parser.DebugRecoveryRuntimeStats()
	if stats.RecoveryEntryCount != 1 || !stats.Enabled || !stats.Completed {
		t.Fatalf("repeated-ladder selected stats = enabled:%v completed:%v entries:%d, want true/true/1", stats.Enabled, stats.Completed, stats.RecoveryEntryCount)
	}
}
