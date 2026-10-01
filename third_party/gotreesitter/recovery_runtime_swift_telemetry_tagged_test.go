//go:build gts_recovery_telemetry

package gotreesitter_test

import (
	"os"
	"path/filepath"
	"testing"

	gotreesitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

func TestSwiftRecoveryAttemptTelemetryTagged(t *testing.T) {
	gotreesitter.EnableRecoveryRuntimeTelemetry(true)
	t.Cleanup(func() { gotreesitter.EnableRecoveryRuntimeTelemetry(false) })

	source, err := os.ReadFile(filepath.Join("grammars", "testdata", "swift_corpus", "stdlib_FloatingPointToString.swift"))
	if err != nil {
		t.Fatalf("read Swift issue 586 witness: %v", err)
	}
	parser := gotreesitter.NewParser(grammars.SwiftLanguage())
	parser.SetAdmissionCandidateRoute(false)
	tree, err := parser.Parse(source)
	if err != nil {
		t.Fatalf("parse Swift issue 586 witness: %v", err)
	}
	defer tree.Release()

	stats := parser.DebugRecoveryRuntimeStats()
	attempts := parser.DebugRecoveryRuntimeAttempts()
	if !stats.Enabled || !stats.Completed || stats.RetryAttemptCount == 0 {
		t.Fatalf("selected-tree receipt = %+v, want completed retry facts", stats)
	}
	if len(attempts) != int(stats.RetryAttemptCount)+1 {
		t.Fatalf("attempt count = %d, want %d: %+v", len(attempts), stats.RetryAttemptCount+1, attempts)
	}
	selected := 0
	for _, attempt := range attempts {
		if attempt.Rung == "" || attempt.Cause == "" || attempt.StopReason == "" || attempt.WallNanos == 0 {
			t.Fatalf("incomplete attempt receipt: %+v", attempt)
		}
		if attempt.CandidateSelected {
			selected++
		}
	}
	if selected == 0 {
		t.Fatalf("attempt receipt has no selected candidate: %+v", attempts)
	}
}
