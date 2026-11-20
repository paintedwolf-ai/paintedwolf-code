package contract

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/toolrejection"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// TestTerminalOpenRequiresProjectRoot keeps the working directory session-scoped.
func TestTerminalOpenRequiresProjectRoot(t *testing.T) {
	bg := bgprocess.NewRegistry(bgprocess.DefaultConfig(), bgprocess.Hooks{})
	reg := tools.NewDefaultRegistry()
	contractcheck.FailErr(t, "register", native.RegisterTerminalSessionTools(reg, bg))
	_, err := reg.Run(context.Background(), "terminal_open", map[string]any{
		"command": "true",
	}, tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "s",
			ProjectID: "p"},
	})
	if err == nil {
		t.Fatal("expected PROJECT_HAS_NO_ROOTS reject")
	}
	var rej *toolrejection.ToolReject
	if !errors.As(err, &rej) || rej.Code != "PROJECT_HAS_NO_ROOTS" {
		t.Fatalf("err = %v, want PROJECT_HAS_NO_ROOTS", err)
	}
	if rej.ArgumentValidation {
		t.Fatal("missing project roots must not report invalid tool arguments")
	}
}
