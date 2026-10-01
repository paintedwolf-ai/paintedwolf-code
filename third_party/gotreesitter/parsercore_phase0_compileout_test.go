//go:build gts_no_parsercorephase0 && !gts_workcount

package gotreesitter

import (
	"bytes"
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestParserCorePhaseZeroDriverCompilesOutOfEmergencyBuild proves the emergency
// opt-out tag gts_no_parsercorephase0 compiles the compact parser-core engine
// back out of the binary. Phase-3 admission promoted the engine into the
// default build; this test guards the escape hatch that removes it entirely.
func TestParserCorePhaseZeroDriverCompilesOutOfEmergencyBuild(t *testing.T) {
	binary := buildEmergencyOptOutTestBinary(t)
	symbols := runGoTool(t, "nm", binary)
	for _, forbidden := range [][]byte{
		[]byte("DiagnosticParseParserCorePrefix"),
		[]byte("newParserCoreFreshFullRunner"),
		[]byte("internal/parsercorephase0.(*Core)"),
	} {
		if bytes.Contains(symbols, forbidden) {
			t.Fatalf("emergency opt-out binary retains tagged parser-core driver symbol %q", forbidden)
		}
	}
}

// buildEmergencyOptOutTestBinary compiles a test binary with the emergency
// opt-out tag so the assertion above verifies the compact engine is absent.
func buildEmergencyOptOutTestBinary(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "gotreesitter.optout.test")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "test", "-c", "-tags", "gts_no_parsercorephase0", "-o", binary, ".")
	output, err := command.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("build emergency opt-out test binary: %v", ctx.Err())
	}
	if err != nil {
		t.Fatalf("build emergency opt-out test binary: %v\n%s", err, output)
	}
	return binary
}
