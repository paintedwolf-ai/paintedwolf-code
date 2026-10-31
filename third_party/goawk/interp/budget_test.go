package interp_test

import (
	"errors"
	"fmt"
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

func (b *countingBudget) Retain(n int) error { b.bytes = 0; return b.Allocate(n) }

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

func TestBudgetReconcilesOnlyDeadRecordTemporaries(t *testing.T) {
	cases := []struct {
		source string
		limit  int
		refuse bool
	}{
		{`{ x=toupper($0) } END {print NR}`, 32768, false},
		{`function temporary(s,a){split(s,a,"");return 1} {temporary($0)} END {print NR}`, 524288, false},
		{`{ x=$0; gsub(/^./,"yyy",x) } END {print NR}`, 32768, false},
		{`function tiny(s){return substr(s,1,4)} { a[NR]=tiny($0) } END {print NR}`, 32768, true},
		{`{ a[NR]=$1 } END {print NR}`, 32768, true},
		{`{ a[NR]=tolower($1) } END {print NR}`, 32768, true},
		{`{ x=x $0 } END {print length(x)}`, 32768, true},
		{`{ a[substr($0,1,4)]++ } END {print length(a)}`, 262144, false},
		{`function tiny(s){return substr(s,1,4)} { a[NR]=tiny($0) "!" } END {print length(a)}`, 262144, false},
	}
	var input strings.Builder
	for i := 0; i < 1000; i++ {
		fmt.Fprintf(&input, "%04d %s\n", i, strings.Repeat("x", 994))
	}
	for _, test := range cases {
		t.Run(test.source, func(t *testing.T) {
			program, err := parser.ParseProgram([]byte(test.source), nil)
			if err != nil {
				t.Fatal(err)
			}
			budget := &countingBudget{maxSteps: 1 << 20, maxBytes: test.limit}
			var out strings.Builder
			_, err = interp.ExecProgram(program, &interp.Config{Stdin: strings.NewReader(input.String()), Output: &out, Environ: []string{}, Budget: budget, MaxInputRecord: 4096})
			if test.refuse {
				if !errors.Is(err, errBudget) {
					t.Fatalf("retained storage accepted: %v", err)
				}
			} else if err != nil || out.String() != "1000\n" {
				t.Fatalf("stream=%q err=%v", out.String(), err)
			}
		})
	}
}

func TestBudgetRefusesReplacementBeforeAssigningPartialArrayValue(t *testing.T) {
	program, err := parser.ParseProgram([]byte(`BEGIN { a[1]="aaa"; r=sprintf("%2000s","x"); print "before"; gsub(/a/,r,a[1]); print "after" }`), nil)
	if err != nil {
		t.Fatal(err)
	}
	machine, err := interp.New(program)
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	_, err = machine.Execute(&interp.Config{Output: &out, Environ: []string{}, Budget: &countingBudget{maxSteps: 1 << 20, maxBytes: 8192}})
	if !errors.Is(err, errBudget) || out.String() != "before\n" || machine.Array("a")["1"] != "aaa" {
		t.Fatalf("err %v output %q values %v", err, out.String(), machine.Array("a"))
	}
}
