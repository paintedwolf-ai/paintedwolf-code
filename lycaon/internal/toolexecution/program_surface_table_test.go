package toolexecution

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
)

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
