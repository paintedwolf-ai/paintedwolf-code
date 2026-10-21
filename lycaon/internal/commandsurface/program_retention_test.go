package commandsurface_test

import (
	"errors"
	"strings"
	"testing"
	"unsafe"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// programMemory sizes working storage for exactly one parser reservation.
func programMemory(t *testing.T, state int64) *toolcontract.ProgramMemory {
	t.Helper()
	memory, err := toolcontract.NewProgramMemory(toolcontract.ProgramMemoryLimits{Working: 32 << 20, State: state})
	testutil.FailErr(t, "program memory", err)
	return memory
}

func stateLimit(err error) bool {
	var limit *toolcontract.ProgramMemoryLimitError
	return errors.As(err, &limit) && limit.Account == "state"
}

func TestExecutionPlanAndDetachedSnapshotRetainIndependentStateLeases(t *testing.T) {
	memory := programMemory(t, 4<<20)
	parser, err := commandsurface.NewProgramParser(t.Context(), memory)
	testutil.FailErr(t, "admit parser memory", err)
	defer parser.Close()
	program, err := parser.Parse([]byte(`printf '%s' 'retained bytes'`), commandsurface.ParseOptions{})
	testutil.FailErr(t, "retain execution plan", err)
	defer program.Close()
	planCharge := memory.InUse().State
	if planCharge <= 0 || memory.InUse().Working != 32<<20 {
		t.Fatal("plan returned without independent memory admission")
	}
	view, err := program.SnapshotWithMemory(t.Context(), memory)
	testutil.FailErr(t, "retain execution snapshot", err)
	defer view.Close()
	if memory.InUse().State != 2*planCharge {
		t.Fatal("snapshot bypassed simultaneous state admission")
	}
	fill, err := memory.ReserveProgramState(t.Context(), 4<<20-memory.InUse().State)
	testutil.FailErr(t, "exhaust aggregate state", err)
	if denied, err := program.SnapshotWithMemory(t.Context(), memory); denied != nil || !stateLimit(err) {
		t.Fatalf("aggregate ceiling did not refuse new snapshot: %v", err)
	}
	fill()
	testutil.FailErr(t, "release parser scratch", parser.Close())
	if memory.InUse().Working != 0 || memory.InUse().State != 2*planCharge {
		t.Fatal("scratch release erased retained plan state")
	}
	testutil.FailErr(t, "release original program", program.Close())
	if memory.InUse().State != planCharge || view.Plan.Version != 1 || len(view.Plan.Statements) != 1 {
		t.Fatal("original release invalidated independent snapshot")
	}
	testutil.FailErr(t, "release detached snapshot", view.Close())
	testutil.FailErr(t, "repeat detached cleanup", view.Close())
	if memory.InUse().State != 0 {
		t.Fatal("released snapshots leaked aggregate accounting")
	}
	if next, err := program.SnapshotWithMemory(t.Context(), memory); next != nil || err == nil {
		t.Fatal("closed program allowed new execution snapshot")
	}
}

func TestStateRefusalReturnsNoExecutionPlanAndKeepsScratchSeparate(t *testing.T) {
	memory := programMemory(t, 1)
	parser, err := commandsurface.NewProgramParser(t.Context(), memory)
	testutil.FailErr(t, "admit scratch before state refusal", err)
	defer parser.Close()
	if program, err := parser.Parse([]byte(`printf changed > ./output`), commandsurface.ParseOptions{}); program != nil || !stateLimit(err) {
		t.Fatalf("state refusal exposed plan: %v", err)
	}
	if memory.InUse().State != 0 || memory.InUse().Working != 32<<20 {
		t.Fatal("refused plan changed independent memory leases")
	}
}

func TestSourceLabelIsBoundedDetachedAndCharged(t *testing.T) {
	backing := strings.Repeat("x", 2<<20)
	label := backing[:6]
	memory := programMemory(t, 4<<20)
	parser, err := commandsurface.NewProgramParser(t.Context(), memory)
	testutil.FailErr(t, "admit parser", err)
	defer parser.Close()
	plain, err := parser.Parse([]byte("true"), commandsurface.ParseOptions{})
	testutil.FailErr(t, "parse without label", err)
	plainCharge := memory.InUse().State
	testutil.FailErr(t, "close plain plan", plain.Close())
	named, err := parser.Parse([]byte("true"), commandsurface.ParseOptions{SourceName: label})
	testutil.FailErr(t, "parse detached label", err)
	defer named.Close()
	snapshot := named.Snapshot()
	if snapshot.SourceName != label || unsafe.StringData(snapshot.SourceName) == unsafe.StringData(label) {
		t.Fatal("source label retains caller backing allocation")
	}
	if memory.InUse().State != plainCharge+int64(len(label)) {
		t.Fatal("source label bypassed retained-state admission")
	}
	if plan, err := parser.Parse([]byte("true"), commandsurface.ParseOptions{SourceName: backing}); plan != nil || err == nil {
		t.Fatal("unbounded source label admitted")
	}
}
