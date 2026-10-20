package toolexecution

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
)

type programVisibility struct{ calls []string }

func (p *programVisibility) Evaluate(_ context.Context, in platform.PolicyContext) (*platform.PolicyDecision, error) {
	p.calls = append(p.calls, in.ToolName)
	return &platform.PolicyDecision{Allowed: in.ToolName == "read"}, nil
}

func TestProgramSurfaceRequiresExplicitVisibleSelection(t *testing.T) {
	registry := tools.NewDefaultRegistry()
	for _, name := range []string{"read", "write"} {
		contract, ok := toolcontract.Lookup(name)
		if !ok {
			t.Fatalf("missing retained contract %s", name)
		}
		testutil.FailErr(t, "register retained fixture", registry.RegisterDefinition(tools.Definition{
			Meta: tools.ToolMeta{Name: name, ArgsSchema: map[string]any{"type": "object"}}, Contract: contract,
			Handler: func(context.Context, map[string]any, tools.ToolContext) (string, error) {
				t.Fatal("discovery executed a handler")
				return "", nil
			},
		}))
	}
	policy := &programVisibility{}
	executor := NewExecutor(policy, registry, "implement")
	offered, definitions, err := executor.Metadata.OfferProgramOperations(t.Context(), platform.ToolFilter{ProfileID: "readonly"}, []string{"read", "write"})
	testutil.FailErr(t, "freeze active operation surface", err)
	if len(definitions) != 1 || definitions[0].Facts().Identity != "read" || len(policy.calls) != 2 {
		t.Fatalf("surface=%v policy=%v", definitions, policy.calls)
	}
	read, _ := registry.Definition("read")
	if definitions[0].Digest() != read.Operation.Digest() {
		t.Fatal("surface recompiled or changed registered definition")
	}
	// Registration cannot make an excluded operation available to direct binding.
	write, _ := registry.Definition("write")
	called := false
	_, err = commandprogram.Invoke(t.Context(), offered, commandprogram.Binding[string, string]{Definition: write.Operation, InvokeOwner: func(context.Context, toolcontract.OperationInvocation, string) (string, error) {
		called = true
		return "", nil
	}}, toolcontract.OperationInvocation{}, "")
	if err == nil || called {
		t.Fatalf("hidden binding err=%v invoked=%v", err, called)
	}
	_, none, err := executor.Metadata.OfferProgramOperations(t.Context(), platform.ToolFilter{ProfileID: "readonly"}, nil)
	testutil.FailErr(t, "freeze empty active surface", err)
	if len(none) != 0 {
		t.Fatal("registry leaked into empty active surface")
	}
	for _, active := range [][]string{{"read", "read"}, {""}, {"unregistered"}} {
		if _, _, err := executor.Metadata.OfferProgramOperations(t.Context(), platform.ToolFilter{ProfileID: "readonly"}, active); err == nil {
			t.Fatalf("invalid active selection admitted %v", active)
		}
	}
	if _, _, err := executor.Metadata.OfferProgramOperations(t.Context(), platform.ToolFilter{}, []string{"read"}); err == nil {
		t.Fatal("implicit profile admitted")
	}
}
