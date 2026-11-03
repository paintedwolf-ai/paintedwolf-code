package native

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/pkg/api"
)

// Boundary refusals have no execution verdict.
func TestBoundaryRefusalIsUnverifiableNotFailed(t *testing.T) {
	t.Parallel()
	for _, refusal := range []string{"subject", "unattributed"} {
		outcome, reason := command.VerdictFor(&hostcmd.Result{
			ExitCode: 1,
			OK:       false,
			Report:   confine.Report{BoundaryRefusal: refusal},
		})
		if outcome != api.SourceVerdictUnverifiable {
			t.Errorf("refusal %q gave outcome %q, want %q", refusal, outcome, api.SourceVerdictUnverifiable)
		}
		if reason != "boundary_refused" {
			t.Errorf("refusal %q gave reason %q, want %q", refusal, reason, "boundary_refused")
		}
	}
}

func TestOrdinaryExitCodesKeepTheirVerdict(t *testing.T) {
	t.Parallel()
	if outcome, reason := command.VerdictFor(&hostcmd.Result{ExitCode: 0, OK: true}); outcome != api.SourceVerdictPassed || reason != "" {
		t.Errorf("clean exit gave (%q, %q), want (%q, \"\")", outcome, reason, api.SourceVerdictPassed)
	}
	if outcome, reason := command.VerdictFor(&hostcmd.Result{ExitCode: 1, OK: false}); outcome != api.SourceVerdictFailed || reason != "" {
		t.Errorf("failing exit gave (%q, %q), want (%q, \"\")", outcome, reason, api.SourceVerdictFailed)
	}
	// Exit 127 is still an observed process failure.
	if outcome, _ := command.VerdictFor(&hostcmd.Result{ExitCode: 127, OK: false}); outcome != api.SourceVerdictFailed {
		t.Errorf("exit 127 gave %q, want %q", outcome, api.SourceVerdictFailed)
	}
}

func rejectCode(t *testing.T, err error) string {
	t.Helper()
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) {
		t.Fatalf("error is not a ToolReject: %v", err)
	}
	return reject.Code
}

// Only the declared command can satisfy its gate.
func TestDeclaredGateRefusesASubstitute(t *testing.T) {
	t.Parallel()
	err := rejectDeclaredCommandOverride("./task check", map[string]any{"command": "go test ./internal/foo/..."})
	if err == nil {
		t.Fatal("verify accepted a substitute for the declared command")
	}
	if code := rejectCode(t, err); code != "VERIFY_DECLARED_COMMAND_OVERRIDE" {
		t.Fatalf("reject code = %q", code)
	}
}

// Declared commands ignore insignificant whitespace.
func TestDeclaredGateAcceptsItsOwnCommand(t *testing.T) {
	t.Parallel()
	for name, args := range map[string]map[string]any{
		"omitted":             {},
		"explicit":            {"command": "./task check"},
		"whitespace variance": {"command": "./task   check"},
	} {
		if err := rejectDeclaredCommandOverride("./task check", args); err != nil {
			t.Errorf("%s: declared command was rejected: %v", name, err)
		}
	}
}

// An undeclared project has no gate to substitute for, so nothing is rejected.
func TestUndeclaredProjectRejectsNothing(t *testing.T) {
	t.Parallel()
	if err := rejectDeclaredCommandOverride("", map[string]any{"command": "run tests"}); err != nil {
		t.Fatalf("undeclared project rejected a check: %v", err)
	}
}

func TestDeclaredGateRefusesChangedArguments(t *testing.T) {
	t.Parallel()
	for _, args := range []map[string]any{
		{"command": `tool 'two words'`},
		{"command": `tool two words`},
	} {
		err := rejectDeclaredCommandOverride(`tool 'two  words'`, args)
		if err == nil {
			t.Fatalf("accepted a different execution: %v", args)
		}
		if code := rejectCode(t, err); code != "VERIFY_DECLARED_COMMAND_OVERRIDE" {
			t.Fatalf("reject code = %q", code)
		}
	}
}
