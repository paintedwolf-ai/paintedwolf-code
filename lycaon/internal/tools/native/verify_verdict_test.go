package native

import (
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// Boundary refusals have no execution verdict.
func TestBoundaryRefusalIsUnverifiableNotFailed(t *testing.T) {
	t.Parallel()
	for _, refusal := range []string{"subject", "unattributed"} {
		outcome, reason := verdictFor(&hostcmd.Result{
			ExitCode: 1,
			OK:       false,
			Report:   confine.Report{BoundaryRefusal: refusal},
		})
		if outcome != VerifyOutcomeUnverifiable {
			t.Errorf("refusal %q gave outcome %q, want %q", refusal, outcome, VerifyOutcomeUnverifiable)
		}
		if reason != unverifiableReasonBoundary {
			t.Errorf("refusal %q gave reason %q, want %q", refusal, reason, unverifiableReasonBoundary)
		}
	}
}

func TestOrdinaryExitCodesKeepTheirVerdict(t *testing.T) {
	t.Parallel()
	if outcome, reason := verdictFor(&hostcmd.Result{ExitCode: 0, OK: true}); outcome != VerifyOutcomePassed || reason != "" {
		t.Errorf("clean exit gave (%q, %q), want (%q, \"\")", outcome, reason, VerifyOutcomePassed)
	}
	if outcome, reason := verdictFor(&hostcmd.Result{ExitCode: 1, OK: false}); outcome != VerifyOutcomeFailed || reason != "" {
		t.Errorf("failing exit gave (%q, %q), want (%q, \"\")", outcome, reason, VerifyOutcomeFailed)
	}
	// Exit 127 is still an observed process failure.
	if outcome, _ := verdictFor(&hostcmd.Result{ExitCode: 127, OK: false}); outcome != VerifyOutcomeFailed {
		t.Errorf("exit 127 gave %q, want %q", outcome, VerifyOutcomeFailed)
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

func TestStampUnverifiableFactsPrependsVerifyCode(t *testing.T) {
	t.Parallel()
	out := &tools.ToolInvocationOut{}
	out.Facts = out.Facts.WithCode(isolation.CodeBoundaryRefused)
	tctx := tools.ToolContext{Out: out}
	stampUnverifiableFacts(tctx, VerifyOutcomeUnverifiable)
	if out.Facts.PrimaryCode() != toolrejection.VerifyUnverifiableCode {
		t.Fatalf("primary = %q want %q", out.Facts.PrimaryCode(), toolrejection.VerifyUnverifiableCode)
	}
	if !out.Facts.HasCode(isolation.CodeBoundaryRefused) {
		t.Fatal("confine code was dropped")
	}
	failed := &tools.ToolInvocationOut{}
	stampUnverifiableFacts(tools.ToolContext{Out: failed}, VerifyOutcomeFailed)
	if failed.Facts.PrimaryCode() != "" {
		t.Fatalf("failed verdict must not stamp: %q", failed.Facts.PrimaryCode())
	}
}

func TestBrokerDeniedVerifyReceiptStaysCompleted(t *testing.T) {
	t.Parallel()
	stamped := confine.StampRefusal("verify", "sess", confine.Boundary{
		Applied: true, Network: confine.NetworkProxyOnly, WriteRoots: []string{"/proj"},
	}, confine.RefusalContext{MediatedNetwork: []confine.EgressHost{{Host: "blocked.test", Allowed: false}}})
	out := &tools.ToolInvocationOut{}
	out.Facts = tools.ApplyRefusalFacts(out.Facts, stamped)
	stampUnverifiableFacts(tools.ToolContext{Out: out}, VerifyOutcomeUnverifiable)
	if out.Facts.Resolution() != api.ToolResultOutcomeCompleted {
		t.Fatalf("outcome = %q want completed", out.Facts.Resolution())
	}
	if out.Facts.PrimaryCode() != toolrejection.VerifyUnverifiableCode {
		t.Fatalf("primary = %q want %q", out.Facts.PrimaryCode(), toolrejection.VerifyUnverifiableCode)
	}
	if !out.Facts.HasCode(isolation.CodeBoundaryRefused) {
		t.Fatal("boundary code was dropped")
	}
	tr := guidance.ComposeToolResult(`{"outcome":"unverifiable","unverifiable_reason":"boundary_refused"}`, out.Facts, nil)
	if tr == nil || tr.Outcome != api.ToolResultOutcomeCompleted {
		t.Fatalf("wire outcome = %v", tr)
	}
	if tr.PrimaryCode() != toolrejection.VerifyUnverifiableCode {
		t.Fatalf("wire primary = %q", tr.PrimaryCode())
	}
}

// An undeclared project has no gate to substitute for, so nothing is rejected.
func TestUndeclaredProjectRejectsNothing(t *testing.T) {
	t.Parallel()
	if err := rejectDeclaredCommandOverride("", map[string]any{"command": "run tests"}); err != nil {
		t.Fatalf("undeclared project rejected a check: %v", err)
	}
}

func TestDeclaredGateRefusesChangedArgumentsAndPipelines(t *testing.T) {
	t.Parallel()
	for _, args := range []map[string]any{
		{"command": `tool 'two words'`},
		{"command": `tool two words`},
		{"pipeline": []any{`tool 'two  words'`}},
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
