package textsearch

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestMatcherSessionsShareAdmissionAndRelease(t *testing.T) {
	for _, dialect := range []Dialect{Basic, Extended, Perl, RE2} {
		t.Run(string(dialect), func(t *testing.T) {
			memory := scanMemoryFixture(t, 1<<20)
			limits := catalogLimits(t)
			limits.Reserve = memory.Reserve
			m, err := Compile(t.Context(), Spec{Patterns: []string{"[ab]*"}, Dialect: dialect}, limits)
			testutil.FailErr(t, "compile admitted regex", err)
			if memory.InUse().Working != 0 {
				t.Fatalf("compilation scratch retained: %+v", memory.InUse())
			}
			before := memory.Reservations()
			_, _, err = m.Select(t.Context(), []byte("aaaa"))
			testutil.FailErr(t, "match admitted record", err)
			if memory.Reservations() <= before || memory.InUse().Working != 0 {
				t.Fatalf("session admission: before=%d after=%d held=%+v", before, memory.Reservations(), memory.InUse())
			}
		})
	}
}

func TestMatcherAdmissionUsesEachCallsContext(t *testing.T) {
	memory := scanMemoryFixture(t, 1<<20)
	limits := catalogLimits(t)
	armed := false
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	limits.Reserve = func(call context.Context, n int64) (func(), error) {
		release, err := memory.Reserve(call, n)
		if armed {
			if call != ctx {
				t.Error("matching retained compilation context")
			}
			cancel()
		}
		return release, err
	}
	m, err := Compile(t.Context(), Spec{Patterns: []string{"a+"}, Dialect: RE2}, limits)
	testutil.FailErr(t, "compile contextual matcher", err)
	armed = true
	_, _, err = m.Select(ctx, []byte("aaaa"))
	if !errors.Is(err, context.Canceled) || memory.InUse().Working != 0 {
		t.Fatalf("call cancellation: err=%v held=%+v", err, memory.InUse())
	}
}
