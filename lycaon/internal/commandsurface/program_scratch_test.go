package commandsurface_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestAdmittedParseChargesActualScratchAndReleasesAfterLowering(t *testing.T) {
	memory := programMemory(t, 4<<20)
	plan, err := commandsurface.ParseAdmitted(t.Context(), memory, []byte("printf '%s' \"$value\""), commandsurface.ParseOptions{})
	testutil.FailErr(t, "parse with admitted scratch", err)
	defer plan.Close()
	peak := memory.Peak().Working
	if peak <= 0 || peak >= 32<<20 {
		t.Fatalf("scratch was not charged incrementally: peak %d", peak)
	}
	if memory.InUse().Working != 0 {
		t.Fatalf("scratch outlived lowering: %+v", memory.InUse())
	}
	if string(plan.Source()) != "printf '%s' \"$value\"" {
		t.Fatal("released scratch invalidated retained host plan")
	}
}

// A long-lived working reservation (for example a background job's stream
// codec) must not refuse a later source parse that fits beside it.
func TestAdmittedParseSharesWorkingAccountWithHeldReservations(t *testing.T) {
	memory := programMemory(t, 4<<20)
	held, err := memory.Reserve(t.Context(), 16<<20)
	testutil.FailErr(t, "hold concurrent working memory", err)
	defer held()
	plan, err := commandsurface.ParseAdmitted(t.Context(), memory, []byte("for x in a b; do printf '%s\\n' \"$x\"; done"), commandsurface.ParseOptions{Sourced: true})
	testutil.FailErr(t, "parse beside held working reservation", err)
	defer plan.Close()
	if memory.InUse().Working != 16<<20 {
		t.Fatalf("parse leaked scratch beside held reservation: %+v", memory.InUse())
	}
}

func TestAdmittedParseRefusesExhaustedAggregateAndCancellation(t *testing.T) {
	memory := programMemory(t, 4<<20)
	held, err := memory.Reserve(t.Context(), 32<<20-1024)
	testutil.FailErr(t, "exhaust working memory", err)
	var limit *toolcontract.ProgramMemoryLimitError
	if p, err := commandsurface.ParseAdmitted(t.Context(), memory, []byte("printf "+strings.Repeat("word ", 64)), commandsurface.ParseOptions{}); p != nil || !errors.As(err, &limit) || limit.Account != "working" {
		t.Fatalf("aggregate refusal=%v", err)
	}
	if memory.InUse().Working != 32<<20-1024 {
		t.Fatalf("refused parse leaked admitted chunks: %+v", memory.InUse())
	}
	held()
	if _, err := commandsurface.ParseAdmitted(t.Context(), nil, []byte(":"), commandsurface.ParseOptions{}); err == nil {
		t.Fatal("parser admitted without aggregate memory")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if plan, err := commandsurface.ParseAdmitted(ctx, memory, []byte("printf should-not-admit"), commandsurface.ParseOptions{}); plan != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled parser admitted plan: %v", err)
	}
	if memory.InUse().Working != 0 {
		t.Fatalf("canceled parse leaked scratch: %+v", memory.InUse())
	}
}

type parserAdmission struct {
	*toolcontract.ProgramMemory
	missingWorking int
	workingCalls   int
	missingState   bool
}

func (a *parserAdmission) Reserve(ctx context.Context, n int64) (func(), error) {
	a.workingCalls++
	if a.workingCalls == a.missingWorking {
		return nil, nil
	}
	return a.ProgramMemory.Reserve(ctx, n)
}
func (a *parserAdmission) ReserveProgramState(ctx context.Context, n int64) (func(), error) {
	if a.missingState {
		return nil, nil
	}
	return a.ProgramMemory.ReserveProgramState(ctx, n)
}

func TestStagedParserRejectsMissingWorkingAndStateLeases(t *testing.T) {
	for _, tc := range []struct {
		name    string
		working int
		state   bool
	}{{"initial working", 1, false}, {"later growth", 2, false}, {"retained state", 0, true}} {
		t.Run(tc.name, func(t *testing.T) {
			memory := programMemory(t, 4<<20)
			admission := &parserAdmission{ProgramMemory: memory, missingWorking: tc.working, missingState: tc.state}
			parser, err := commandsurface.NewProgramParser(t.Context(), admission)
			if tc.working == 1 {
				if parser != nil || err == nil {
					t.Fatal("missing initial lease admitted parser")
				}
			} else {
				testutil.FailErr(t, "admit staged parser", err)
				source := []byte(":")
				if tc.working == 2 {
					source = []byte(strings.Repeat("printf word; ", 128))
				}
				if parsed, err := parser.Parse(source, commandsurface.ParseOptions{}); parsed != nil || err == nil {
					t.Fatal("missing growth/state lease admitted plan")
				}
				testutil.FailErr(t, "release refused parser", parser.Close())
			}
			if memory.InUse() != (toolcontract.ProgramMemoryUsage{}) {
				t.Fatalf("refusal leaked leases: %+v", memory.InUse())
			}
		})
	}
}

func TestStagedParserAdmissionAndSnapshotLifetimes(t *testing.T) {
	memory := programMemory(t, 4<<20)
	held, err := memory.Reserve(t.Context(), 16<<20)
	testutil.FailErr(t, "retain another working user", err)
	defer held()
	parser, err := commandsurface.NewProgramParser(t.Context(), memory)
	testutil.FailErr(t, "admit initial parser chunk", err)
	if usage := memory.InUse().Working; usage <= 16<<20 || usage >= 32<<20 {
		t.Fatalf("initial scratch did not share account: %d", usage)
	}
	parsed, err := parser.Parse([]byte(":"), commandsurface.ParseOptions{Sourced: true})
	testutil.FailErr(t, "parse beside another working lease", err)
	if repeated, err := parser.Parse([]byte(":"), commandsurface.ParseOptions{}); repeated != nil || err == nil {
		t.Fatal("consumed parser admitted another unit")
	}
	testutil.FailErr(t, "release parser scratch", parser.Close())
	testutil.FailErr(t, "repeat scratch close", parser.Close())
	if memory.InUse().Working != 16<<20 {
		t.Fatalf("scratch outlived parse: %+v", memory.InUse())
	}
	missing := &parserAdmission{ProgramMemory: memory, missingState: true}
	if snapshot, err := parsed.SnapshotWithMemory(t.Context(), missing); snapshot != nil || err == nil {
		t.Fatal("snapshot accepted missing state release")
	}
	snapshot, err := parsed.SnapshotWithMemory(t.Context(), memory)
	testutil.FailErr(t, "admit execution snapshot", err)
	testutil.FailErr(t, "release original plan", parsed.Close())
	if memory.InUse().State == 0 {
		t.Fatal("snapshot lost its retained lease")
	}
	testutil.FailErr(t, "release detached snapshot", snapshot.Close())
	if memory.InUse().State != 0 {
		t.Fatalf("snapshot retained state: %+v", memory.InUse())
	}
}
