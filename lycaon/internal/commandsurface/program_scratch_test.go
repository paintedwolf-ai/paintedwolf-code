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
