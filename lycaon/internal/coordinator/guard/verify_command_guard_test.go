package guard_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/oar"
)

func TestPrepareVerifyCallInjectsDeclaredWhenBare(t *testing.T) {
	args := map[string]any{}
	guard.PrepareVerifyCall("verify", "./task check-fast", args)
	if args["command"] != "./task check-fast" {
		t.Fatalf("declared command not injected: %v", args["command"])
	}
}

func TestObserveVerifyCommandUndeclared_bare(t *testing.T) {
	args := map[string]any{}
	guard.PrepareVerifyCall("verify", "  ", args)
	gc := oar.NewGuardContext()
	guard.ObserveVerifyCommandUndeclared("verify", "  ", args, gc)
	if !guard.EvaluateObserveHasCode(gc, oar.AnchorCoordinatorPreInvoke, guard.VerifyCommandUndeclaredCode) {
		t.Fatalf("want VERIFY_COMMAND_UNDECLARED Decision, facts verify_has_command=%v verify_declared=%v",
			gc.Progress.VerifyHasCommand, gc.Progress.VerifyDeclared)
	}
	if _, ok := args["command"]; ok {
		t.Fatal("must not invent a command on reject")
	}
}

func TestPrepareVerifyCallPassesThroughExplicitCommand(t *testing.T) {
	args := map[string]any{"command": "go test ./..."}
	guard.PrepareVerifyCall("verify", "./task check-fast", args)
	if args["command"] != "go test ./..." {
		t.Fatalf("explicit command must not be overwritten: %v", args["command"])
	}
	gc := oar.NewGuardContext()
	guard.ObserveVerifyCommandUndeclared("verify", "./task check-fast", args, gc)
	if guard.EvaluateObserveHasCode(gc, oar.AnchorCoordinatorPreInvoke, guard.VerifyCommandUndeclaredCode) {
		t.Fatalf("explicit command must not observe VERIFY_COMMAND_UNDECLARED")
	}
}

func TestPrepareVerifyCallIgnoresNonVerifyTool(t *testing.T) {
	args := map[string]any{}
	guard.PrepareVerifyCall("command", "", args)
	if len(args) != 0 {
		t.Fatalf("non-verify tool must be untouched: %#v", args)
	}
	_ = strings.TrimSpace
}
