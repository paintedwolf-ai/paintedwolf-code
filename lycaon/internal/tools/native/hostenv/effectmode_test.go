//go:build darwin || linux

package hostenv

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestBuiltinEffectModeRetainsEnvInspectionAndDispatch(t *testing.T) {
	h := newHarness(t, defaultHost())
	builtin := h.owners.Builtins(h.tc, nil, nil)["env"]
	modeOwner, ok := builtin.(interface {
		EffectMode(context.Context, commandprogram.OperationRequest) string
	})
	if !ok {
		t.Fatal("host environment builtin lost the program dispatch mode contract")
	}
	for _, c := range []struct {
		words []string
		mode  string
	}{
		{[]string{"env"}, "inspect"},
		{[]string{"env", "VALUE=one"}, "inspect"},
		{[]string{"env", "VALUE=one", "pwd"}, "dispatch"},
	} {
		request := commandprogram.OperationRequest{OperationID: "env", Invocation: toolcontract.OperationInvocation{Kind: toolcontract.ProgramChildInvocation, Identity: toolcontract.ExecutionIdentity{ExecutionID: "mode"}, Span: toolcontract.SourceSpan{Line: 1, Column: 1}}}
		request.Statement.Span = request.Invocation.Span
		for _, word := range c.words {
			request.Argv = append(request.Argv, commandprogram.Field{Value: word})
		}
		if got := modeOwner.EffectMode(t.Context(), request); got != c.mode {
			t.Fatalf("%v mode=%q, want %q", c.words, got, c.mode)
		}
	}
}
