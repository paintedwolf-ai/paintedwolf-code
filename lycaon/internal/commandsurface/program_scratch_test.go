package commandsurface_test

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestProgramParserReservesThroughLoweringAndClosesOnce(t *testing.T) {
	memory := programMemory(t, 4<<20)
	p, err := commandsurface.NewProgramParser(t.Context(), memory)
	testutil.FailErr(t, "reserve parser", err)
	defer p.Close()
	if memory.InUse().Working != 32<<20 {
		t.Fatalf("wrong parser reservation: %+v", memory.InUse())
	}
	plan, err := p.Parse([]byte("printf '%s' \"$value\""), commandsurface.ParseOptions{})
	testutil.FailErr(t, "parse with reserved scratch", err)
	defer plan.Close()
	if memory.InUse().Working != 32<<20 || plan == nil {
		t.Fatal("reservation ended before host lowering")
	}
	testutil.FailErr(t, "release parser", p.Close())
	testutil.FailErr(t, "repeat parser close", p.Close())
	if memory.InUse().Working != 0 {
		t.Fatalf("scratch not released exactly once: %+v", memory.InUse())
	}
	if result, err := p.Parse([]byte(":"), commandsurface.ParseOptions{}); result != nil || err == nil {
		t.Fatal("closed reservation permitted parsing")
	}
	if string(plan.Source()) != "printf '%s' \"$value\"" {
		t.Fatal("released scratch invalidated retained host plan")
	}
}

func TestProgramParserCancellationAndAdmissionRefusal(t *testing.T) {
	memory := programMemory(t, 4<<20)
	held, err := memory.Reserve(t.Context(), 1)
	testutil.FailErr(t, "hold working memory", err)
	var limit *toolcontract.ProgramMemoryLimitError
	if p, err := commandsurface.NewProgramParser(t.Context(), memory); p != nil || !errors.As(err, &limit) || limit.Account != "working" {
		t.Fatalf("admission refusal=%v", err)
	}
	held()
	if _, err := commandsurface.NewProgramParser(t.Context(), nil); err == nil {
		t.Fatal("parser admitted without aggregate memory")
	}
	ctx, cancel := context.WithCancel(t.Context())
	p, err := commandsurface.NewProgramParser(ctx, memory)
	testutil.FailErr(t, "reserve cancelable parser", err)
	defer p.Close()
	cancel()
	if plan, err := p.Parse([]byte("printf should-not-admit"), commandsurface.ParseOptions{}); plan != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled parser admitted plan: %v", err)
	}
}
