package interp_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/benhoyt/goawk/interp"
	"github.com/benhoyt/goawk/parser"
)

type countingBudget struct {
	steps, bytes, maxSteps, maxBytes int
}

var errBudget = errors.New("budget exhausted")

func (b *countingBudget) Step() error {
	b.steps++
	if b.steps > b.maxSteps {
		return errBudget
	}
	return nil
}

func (b *countingBudget) Allocate(n int) error {
	b.bytes += n
	if b.bytes > b.maxBytes {
		return errBudget
	}
	return nil
}

func run(t *testing.T, src string, budget *countingBudget) (string, error) {
	t.Helper()
	program, err := parser.ParseProgram([]byte(src), nil)
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	_, err = interp.ExecProgram(program, &interp.Config{Stdin: strings.NewReader("a b\n"), Output: &out, Environ: []string{}, Budget: budget})
	return out.String(), err
}

func TestBudgetMetersBackwardJumpsCallsAndIterations(t *testing.T) {
	for _, c := range []struct {
		src   string
		steps int
	}{
		// Loops test their condition at the bottom: n iterations jump back n-1 times.
		{"BEGIN { for (i = 0; i < 5; i++) ; }", 4},
		{"BEGIN { i = 0; while (i < 3) i++ }", 2},
		{"BEGIN { i = 0; do i++; while (i < 4) }", 3},
		{"BEGIN { a[1]; a[2]; for (k in a) ; }", 2},
		{"function f() { return 1 } BEGIN { f(); f() }", 2},
		{"BEGIN { print 1 }", 0},
	} {
		budget := &countingBudget{maxSteps: 1 << 20, maxBytes: 1 << 20}
		if _, err := run(t, c.src, budget); err != nil || budget.steps != c.steps {
			t.Errorf("%s: steps %d err %v, want %d", c.src, budget.steps, err, c.steps)
		}
	}
	if _, err := run(t, "BEGIN { while (1) ; }", &countingBudget{maxSteps: 10, maxBytes: 1 << 20}); !errors.Is(err, errBudget) {
		t.Fatalf("unbounded loop: %v", err)
	}
}

func TestBudgetMetersCreatedValues(t *testing.T) {
	budget := &countingBudget{maxSteps: 1 << 20, maxBytes: 1 << 20}
	out, err := run(t, `{ s = $1 "-" $2; t = toupper(s); n = split("x,yy", parts, ","); gsub(/a/, "AA", s); printf "%s %s %d\n", s, t, n; a["k"] = "v" }`, budget)
	if err != nil || out != "AA-b A-B 2\n" {
		t.Fatalf("out %q err %v", out, err)
	}
	// ConcatMulti 3, toupper 3, split 1+2+128 overhead, gsub 4, sprintf bound, element 1+1+64.
	if budget.bytes < 3+3+3+128+4+65 {
		t.Fatalf("metered %d bytes", budget.bytes)
	}
	if _, err := run(t, `BEGIN { printf "%100000d", 1 }`, &countingBudget{maxSteps: 1, maxBytes: 1000}); !errors.Is(err, errBudget) {
		t.Fatalf("wide format: %v", err)
	}
}
