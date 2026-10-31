package commandprogram

import (
	"context"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestArrayAssignmentsAdmitBeforeCopyAndKeepOldState(t *testing.T) {
	memory, err := toolcontract.NewProgramMemory(toolcontract.ProgramMemoryLimits{Working: 1 << 20, State: 1 << 20})
	testutil.FailErr(t, "create memory", err)
	vars, err := NewVariables(t.Context(), memory, "inline", nil, nil)
	testutil.FailErr(t, "create variables", err)
	defer vars.Close()
	testutil.FailErr(t, "seed sparse array", vars.AssignIndex(t.Context(), "a", 4, "old", false))
	held := memory.InUse().State
	release, err := memory.ReserveProgramState(t.Context(), (1<<20)-held)
	testutil.FailErr(t, "occupy state", err)
	for _, change := range []func() error{
		func() error { return vars.AssignIndex(t.Context(), "a", 2, "new", false) },
		func() error { return vars.AssignArray(t.Context(), "a", []string{"new"}, false) },
		func() error { return vars.AssignArray(t.Context(), "a", []string{"new"}, true) },
	} {
		var refused *toolcontract.ProgramMemoryLimitError
		if err := change(); !errors.As(err, &refused) {
			t.Fatalf("replacement admission: %v", err)
		}
		values, _ := vars.VariableElements("a")
		if !reflect.DeepEqual(values, []string{"old"}) {
			t.Fatalf("refusal changed array: %q", values)
		}
	}
	release()
	if memory.InUse().State != held {
		t.Fatalf("refusal leaked charge: %+v", memory.InUse())
	}
	testutil.FailErr(t, "append sparse array", vars.AssignArray(t.Context(), "a", []string{"next"}, true))
	if value, ok := vars.VariableElement("a", 5); !ok || value != "next" {
		t.Fatalf("append lost sparse index: %q %v", value, ok)
	}
	testutil.FailErr(t, "set maximum index", vars.AssignIndex(t.Context(), "a", math.MaxInt64, "last", false))
	var overflow *ExpansionError
	if err := vars.AssignArray(t.Context(), "a", []string{"overflow"}, true); !errors.As(err, &overflow) || overflow.Limit != "array_index" || overflow.Maximum != math.MaxInt64 || overflow.ObservedValue() != uint64(math.MaxInt64)+1 {
		t.Fatalf("append overflow facts: %+v, error=%v", overflow, err)
	}
	testutil.FailErr(t, "empty append at maximum index", vars.AssignArray(t.Context(), "a", nil, true))
	if _, ok := vars.VariableElement("a", math.MaxInt64); !ok {
		t.Fatal("maximum index lost")
	}
}

func TestVariableMutationRefusesBeforeLargeAllocation(t *testing.T) {
	memory := testMemory(t)
	vars := testScope(t, memory, nil)
	items := make([]string, 8192)
	for i := range items {
		items[i] = "value"
	}
	testutil.FailErr(t, "seed large array", vars.AssignArray(t.Context(), "a", items, false))
	testutil.FailErr(t, "seed scalar", vars.Assign(t.Context(), "s", strings.Repeat("v", 1<<15)))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	before := memory.InUse()
	operations := map[string]func() error{
		"index":  func() error { return vars.AssignIndex(ctx, "a", 0, "x", true) },
		"array":  func() error { return vars.AssignArray(ctx, "a", items, true) },
		"scalar": func() error { return vars.AssignScalar(ctx, "s", "x", true) },
		"clone":  func() error { _, err := vars.Clone(ctx); return err },
	}
	for name, operation := range operations {
		t.Run(name, func(t *testing.T) {
			if err := operation(); !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled admission: %v", err)
			}
			// A refused operation may construct its error/closure, but must not copy
			// thousands of array entries or concatenate the retained scalar first.
			if allocations := testing.AllocsPerRun(5, func() { _ = operation() }); allocations > 12 {
				t.Fatalf("refusal allocated %.0f objects", allocations)
			}
		})
	}
	if got := memory.InUse(); got != before {
		t.Fatalf("refusal retained memory: %+v -> %+v", before, got)
	}
}

func TestVariableProjectionsAdmitOverlapBeforeAllocation(t *testing.T) {
	memory, err := toolcontract.NewProgramMemory(toolcontract.ProgramMemoryLimits{Working: 64, State: 1 << 20})
	testutil.FailErr(t, "create memory", err)
	vars, err := NewVariables(t.Context(), memory, "inline", []string{"EXPORTED=" + strings.Repeat("v", 256)}, []string{"one"})
	testutil.FailErr(t, "create variables", err)
	defer vars.Close()
	held := memory.InUse()
	var refused *toolcontract.ProgramMemoryLimitError
	if values, release, err := vars.Environment(t.Context(), nil); !errors.As(err, &refused) || values != nil || release != nil {
		t.Fatalf("environment projection admission: %q %v", values, err)
	}
	release, err := memory.ReserveProgramState(t.Context(), (1<<20)-held.State)
	testutil.FailErr(t, "occupy retained state", err)
	if err := vars.SetPositional(t.Context(), []string{"two"}); !errors.As(err, &refused) {
		t.Fatalf("positional overlap admission: %v", err)
	}
	if !reflect.DeepEqual(vars.Positional(), []string{"one"}) {
		t.Fatalf("refusal replaced positional values: %q", vars.Positional())
	}
	if child, err := vars.Clone(t.Context()); !errors.As(err, &refused) || child != nil {
		t.Fatalf("clone overlap admission: %v", err)
	}
	release()
	if memory.InUse() != held {
		t.Fatalf("projection refusal retained memory: %+v", memory.InUse())
	}
}
