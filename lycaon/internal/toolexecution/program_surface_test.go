package toolexecution

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

type programVisibility struct{ calls []string }

func (*programVisibility) Evaluate(context.Context, platform.PolicyContext) (*platform.PolicyDecision, error) {
	return &platform.PolicyDecision{Allowed: false}, nil
}
func (p *programVisibility) CommandOperationAllowed(_ context.Context, _, identity, _ string) (bool, error) {
	p.calls = append(p.calls, identity)
	return identity == "read", nil
}

func nativeDefinition(t *testing.T, name string) toolcontract.OperationDefinition {
	t.Helper()
	contract, ok := toolcontract.Lookup(name)
	if !ok {
		t.Fatalf("missing retained owner contract %s", name)
	}
	definition, err := toolcontract.ToolOperation(name, json.RawMessage(`{"type":"object"}`), contract)
	testutil.FailErr(t, "compile actual public native definition", err)
	return definition
}

func TestProgramSurfaceRequiresExplicitNativeSelection(t *testing.T) {
	// No JSON handlers or schemas are registered. They neither expose a hidden
	// operation nor control whether these typed native bindings can be offered.
	executor := NewExecutor(&programVisibility{}, tools.NewDefaultRegistry(), "fixture")
	read, write := nativeDefinition(t, "read"), nativeDefinition(t, "write")
	selection, err := executor.Metadata.SelectNativeOperations(t.Context(), platform.ToolFilter{ProfileID: "fixture"}, []toolcontract.OperationDefinition{read, write})
	testutil.FailErr(t, "freeze explicit native bindings", err)
	definitions := selection.Definitions()
	if len(definitions) != 1 || definitions[0].Digest() != read.Digest() {
		t.Fatalf("native surface=%v", definitions)
	}
	if err := selection.offered.Validate(write, toolcontract.OperationInvocation{}); err == nil {
		t.Fatal("excluded native binding became callable")
	}
	definitions[0] = write
	if selection.Definitions()[0].Digest() != read.Digest() {
		t.Fatal("presentation changed frozen native selection")
	}
	empty, err := executor.Metadata.SelectNativeOperations(t.Context(), platform.ToolFilter{ProfileID: "fixture"}, nil)
	testutil.FailErr(t, "freeze empty native selection", err)
	if len(empty.Definitions()) != 0 {
		t.Fatal("registration leaked into empty native surface")
	}
	for _, active := range [][]toolcontract.OperationDefinition{{read, read}, {nativeDefinition(t, "update_progress")}, {toolcontract.OperationDefinition{}}} {
		if _, err := executor.Metadata.SelectNativeOperations(t.Context(), platform.ToolFilter{ProfileID: "fixture"}, active); err == nil {
			t.Fatal("invalid or hidden structured-only binding admitted")
		}
	}
	if _, err := executor.Metadata.SelectNativeOperations(t.Context(), platform.ToolFilter{}, []toolcontract.OperationDefinition{read}); err == nil {
		t.Fatal("implicit profile admitted")
	}
}

func TestReadOnlyProgramSurfaceNarrowsTheDeclaredProfile(t *testing.T) {
	const profile = "fixture"
	boundary := sandbox.NewBoundary(sandbox.Config{}, []sandbox.ToolProfile{
		{ID: profile, CommandEffects: toolcontract.AllCommandEffects()},
	})
	executor := NewExecutor(toolprofiles.NewProfilePolicyEngine(boundary), tools.NewDefaultRegistry(), profile)
	read := nativeDefinition(t, "read")
	write := nativeDefinition(t, "write")
	surface, err := executor.Metadata.FreezeProgramSurface(t.Context(), platform.ToolFilter{ProfileID: profile}, []toolcontract.OperationDefinition{read, write})
	testutil.FailErr(t, "freeze declared program surface", err)

	invocation, _ := frameFixture(t)
	readOnly := surface.ReadOnly()
	for _, binding := range []struct {
		def        toolcontract.OperationDefinition
		wantDenied bool
	}{
		{def: read},
		{def: write, wantDenied: true},
	} {
		form := binding.def.Facts().Commands[0]
		operation, err := toolcontract.BindOperation(binding.def, invocation, form.Name, form.Effects[0].Mode, nil)
		testutil.FailErr(t, "bind catalog operation", err)
		testutil.FailErr(t, "validate full profile", surface.ValidateOperation(operation))
		err = readOnly.ValidateOperation(operation)
		if binding.wantDenied {
			if reject := toolrejection.AsToolReject(err); reject == nil || reject.Code != "COMMAND_EFFECT_NOT_ALLOWED" {
				t.Fatalf("write-capable operation admitted by read-only surface: %v", err)
			}
		} else {
			testutil.FailErr(t, "validate read operation", err)
		}
	}
	full := toolcontract.AllCommandEffects()
	if surface.Effects() != full || readOnly.AllowsEffects(full) {
		t.Fatalf("full effects = %v, read-only effects = %v", surface.Effects(), readOnly.Effects())
	}
}

type countingProgramPolicy struct {
	programEffectPolicy
	allowedCalls *atomic.Int64
}

func (p countingProgramPolicy) CommandOperationAllowed(context.Context, string, string, string) (bool, error) {
	p.allowedCalls.Add(1)
	return true, nil
}

func TestProgramSurfaceTableIsTheTurnGuardsTable(t *testing.T) {
	all, _ := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly, toolcontract.CommandMutation, toolcontract.CommandExternalExec, toolcontract.CommandNetworkEgress)
	calls := &atomic.Int64{}
	executor := NewExecutor(countingProgramPolicy{programEffectPolicy{effects: all}, calls}, tools.NewDefaultRegistry(), "fixture")
	executor.Metadata.SetCommandExternalHidden(true)
	filter := platform.ToolFilter{ProfileID: "fixture"}

	surface, err := executor.Metadata.FreezeProgramSurface(t.Context(), filter, nil)
	testutil.FailErr(t, "freeze surface", err)
	if calls.Load() != 0 {
		t.Fatalf("freezing the surface built its table: %d policy calls", calls.Load())
	}
	fromSurface, err := surface.ProgramTable(t.Context())
	testutil.FailErr(t, "surface table", err)
	built := calls.Load()
	again, err := surface.ProgramTable(t.Context())
	testutil.FailErr(t, "surface table again", err)
	if again != fromSurface || calls.Load() != built {
		t.Fatalf("second stream rebuilt the table: calls %d -> %d", built, calls.Load())
	}
	guard, err := executor.Metadata.ProgramTable(t.Context(), filter)
	testutil.FailErr(t, "turn guard table", err)

	catalog, err := toolcontract.ReadOnlyLanguageV1()
	testutil.FailErr(t, "catalog", err)
	for _, spec := range catalog.Commands {
		if fromSurface.Program(spec.Name) != guard.Program(spec.Name) {
			t.Errorf("%s: surface program reach %t, turn guard %t", spec.Name, fromSurface.Program(spec.Name), guard.Program(spec.Name))
		}
		for _, mode := range spec.Effects {
			if fromSurface.Mode(spec.Name, mode.Mode) != guard.Mode(spec.Name, mode.Mode) {
				t.Errorf("%s %s: surface %t, turn guard %t", spec.Name, mode.Mode, fromSurface.Mode(spec.Name, mode.Mode), guard.Mode(spec.Name, mode.Mode))
			}
		}
	}
	if fromSurface.Program("external") {
		t.Fatal("hidden external selector reached through the output-delivery table")
	}
}

func TestZeroProgramSurfaceHasNoTable(t *testing.T) {
	if table, err := (ProgramSurface{}).ProgramTable(t.Context()); err == nil || table != nil {
		t.Fatalf("zero surface table = %v, err = %v", table, err)
	}
}
