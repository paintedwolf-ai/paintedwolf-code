package toolhost

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

// TestCommandLiteralReadersRefuseUnsupportedOptionsWithoutExternalFallback
// verifies Q21/F02: exact-mapped literal document readers refuse unsupported
// options with COMMAND_OPTION_UNSUPPORTED and zero external launch even when
// external execution is enabled in the implement profile, while preserving
// deliberate `external --` execution.
func TestCommandLiteralReadersRefuseUnsupportedOptionsWithoutExternalFallback(t *testing.T) {
	f := newComposedProgramFixture(t)
	testutil.FailErr(t, "write input.txt", os.WriteFile(filepath.Join(f.Dir, "input.txt"), []byte("alpha\n"), 0o644))

	// Positive control: deliberate external execution remains enabled and finds cat.
	f.Configure = func(call *tools.ToolContext) {
		call.Identity.Agent = "implement"
	}
	raw, err := f.Call(t, t.Context(), "external -- cat ./input.txt")
	testutil.FailErr(t, "run deliberate external cat", err)
	if ext := decodeProgramOutcome(t, raw); ext.Refusal != nil || ext.Status != 0 || ext.Stdout != "alpha\n" {
		t.Fatalf("deliberate external cat = %+v, refusal=%+v", ext, ext.Refusal)
	}

	for _, tc := range []struct {
		name   string
		source string
		word   string
	}{
		{name: "cat unsupported option", source: "cat --not-a-version-1-option ./input.txt", word: "--not-a-version-1-option"},
		{name: "head unsupported option", source: "head --not-a-version-1-option ./input.txt", word: "--not-a-version-1-option"},
		{name: "tail unsupported follow", source: "tail -f ./input.txt", word: "-f"},
		{name: "nl unsupported option", source: "nl --not-a-version-1-option ./input.txt", word: "--not-a-version-1-option"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := &tools.ToolInvocationOut{}
			f.Configure = func(call *tools.ToolContext) {
				call.Identity.Agent = "implement"
				call.Effects.Out = out
			}
			raw, err := f.Call(t, t.Context(), tc.source)
			testutil.FailErr(t, tc.source, err)
			result := decodeProgramOutcome(t, raw)
			if result.State != "rejected" || result.Status != 2 || result.StopCode != "COMMAND_OPTION_UNSUPPORTED" {
				t.Fatalf("outcome=%+v", result)
			}
			if result.Refusal == nil || result.Refusal.Code != "COMMAND_OPTION_UNSUPPORTED" || result.Refusal.Details["word"] != tc.word {
				t.Fatalf("refusal=%+v, want COMMAND_OPTION_UNSUPPORTED for %q", result.Refusal, tc.word)
			}
			if result.NativeGaps != 0 || result.ExecutedChildren != 0 || result.CompletedEffects != 0 || result.UncertainEffects != 0 || result.SourceWindow != nil {
				t.Fatalf("unexpected external launch or effects: %+v", result)
			}
			if out.ProgramRefusal == nil || out.ProgramRefusal.Code != "COMMAND_OPTION_UNSUPPORTED" || !out.ProgramRefusal.BeforeAdmission {
				t.Fatalf("program refusal visibility = %+v", out.ProgramRefusal)
			}
			if !strings.Contains(result.Stderr, "[COMMAND_OPTION_UNSUPPORTED]") {
				t.Fatalf("stderr %q missing COMMAND_OPTION_UNSUPPORTED", result.Stderr)
			}
			var externalOrStarted int
			err = f.Database.QueryRowContext(
				t.Context(),
				`SELECT COUNT(*) FILTER (WHERE operation_id='command' OR native_gap_json != '' OR owner_started=1) FROM invocation_program_executions WHERE program_id=?`,
				result.ProgramID,
			).Scan(&externalOrStarted)
			testutil.FailErr(t, "query child receipts", err)
			if externalOrStarted != 0 {
				t.Fatalf("recorded %d external/gap/started child rows for %q, want 0", externalOrStarted, tc.source)
			}
		})
	}

	t.Run("nested env cat unsupported option", func(t *testing.T) {
		f.Configure = func(call *tools.ToolContext) {
			call.Identity.Agent = "implement"
		}
		raw, err := f.Call(t, t.Context(), "env cat --not-a-version-1-option ./input.txt")
		testutil.FailErr(t, "env cat", err)
		result := decodeProgramOutcome(t, raw)
		if result.State != "rejected" || result.Refusal == nil || result.Refusal.Code != "COMMAND_OPTION_UNSUPPORTED" || result.NativeGaps != 0 || result.SourceWindow != nil {
			t.Fatalf("nested env cat outcome=%+v refusal=%+v", result, result.Refusal)
		}
		var externalOrGap int
		err = f.Database.QueryRowContext(
			t.Context(),
			`SELECT COUNT(*) FILTER (WHERE operation_id='command' OR native_gap_json != '') FROM invocation_program_executions WHERE program_id=?`,
			result.ProgramID,
		).Scan(&externalOrGap)
		testutil.FailErr(t, "query nested child receipts", err)
		if externalOrGap != 0 {
			t.Fatalf("recorded %d external/gap rows for nested env cat, want 0", externalOrGap)
		}
	})
}
