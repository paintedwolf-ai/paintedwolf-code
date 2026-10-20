package commandsurface_test

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/testutil"
)

type scratchAdmissionFunc func(context.Context, int64) (func(), error)

func (f scratchAdmissionFunc) ReserveParserScratch(ctx context.Context, n int64) (func(), error) {
	return f(ctx, n)
}

func TestProgramParserReservesThroughLoweringAndClosesOnce(t *testing.T) {
	reserved, released := false, 0
	p, err := commandsurface.NewProgramParser(t.Context(), scratchAdmissionFunc(func(_ context.Context, n int64) (func(), error) {
		if n != 32<<20 {
			t.Fatalf("wrong parser reservation: %d", n)
		}
		reserved = true
		return func() { released++ }, nil
	}))
	testutil.FailErr(t, "reserve parser", err)
	defer p.Close()
	plan, err := p.Parse([]byte("printf '%s' \"$value\""), commandsurface.ParseOptions{})
	testutil.FailErr(t, "parse with reserved scratch", err)
	if !reserved || released != 0 || plan == nil {
		t.Fatal("reservation ended before host lowering")
	}
	testutil.FailErr(t, "release parser", p.Close())
	testutil.FailErr(t, "repeat parser close", p.Close())
	if released != 1 {
		t.Fatalf("scratch released %d times", released)
	}
	if result, err := p.Parse([]byte(":"), commandsurface.ParseOptions{}); result != nil || err == nil {
		t.Fatal("closed reservation permitted parsing")
	}
	if string(plan.Source()) != "printf '%s' \"$value\"" {
		t.Fatal("released scratch invalidated retained host plan")
	}
}

func TestProgramParserCancellationAndAdmissionRefusal(t *testing.T) {
	refused := errors.New("synthetic aggregate scratch ceiling")
	if p, err := commandsurface.NewProgramParser(t.Context(), scratchAdmissionFunc(func(context.Context, int64) (func(), error) { return nil, refused })); p != nil || !errors.Is(err, refused) {
		t.Fatalf("admission refusal=%v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	p, err := commandsurface.NewProgramParser(ctx, scratchAdmissionFunc(func(context.Context, int64) (func(), error) { return func() {}, nil }))
	testutil.FailErr(t, "reserve cancelable parser", err)
	defer p.Close()
	cancel()
	if plan, err := p.Parse([]byte("printf should-not-admit"), commandsurface.ParseOptions{}); plan != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled parser admitted plan: %v", err)
	}
}

func (f scratchAdmissionFunc) ReserveProgramState(context.Context, int64) (func(), error) {
	return func() {}, nil
}
