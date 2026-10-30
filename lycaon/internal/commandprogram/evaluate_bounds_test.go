package commandprogram

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestPipelineKeepsEveryProducerOutcome(t *testing.T) {
	cases := []struct {
		name    string
		program string
		status  int
		stdout  string
		stdin   map[string]string
	}{
		{"bytes flow between stages", "echo hi | cat | cat", 0, "hi\n", map[string]string{"cat": "hi\n"}},
		{"earlier producer failure survives later success", "fail 3 | cat", 3, "", nil},
		{"rightmost nonzero wins", "fail 3 | external -- fail 4 | cat", 4, "", nil},
		{"middle failure under successful ends", "echo a | external -- fail 7 | echo z", 7, "z\n", nil},
		{"native comparison status", "echo a | grep z", 1, "", nil},
		{"negated pipeline", "! echo a | grep z", 0, "", nil},
		{"native early consumer closure is expected", "cat <<EOF | head -n 1\none\ntwo\nthree\nEOF\n", 0, "one\n", map[string]string{"head": "one\n"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newEvalFixture(t)
			out, err := f.run(t, t.Context(), c.program)
			if err != nil || out.Stop != nil {
				t.Fatalf("evaluate: %v stop=%+v", err, out.Stop)
			}
			if out.Status != c.status || f.stdout.String() != c.stdout {
				t.Fatalf("status=%d stdout=%q", out.Status, f.stdout.String())
			}
			for operation, stdin := range c.stdin {
				if got := f.log.find(t, operation).stdin; got != stdin {
					t.Fatalf("%s read %q, want %q", operation, got, stdin)
				}
			}
			if f.log.forks != f.log.released {
				t.Fatalf("stage directories leaked: %d/%d", f.log.forks, f.log.released)
			}
		})
	}
}

func TestPipelineExternalClosureIsNotNativeClosure(t *testing.T) {
	f := newEvalFixture(t)
	out, err := f.run(t, t.Context(), "external -- yes | head -n 1")
	testutil.FailErr(t, "run evaluated command program", err)
	// Only the executor's own SIGPIPE classification may normalize an external
	// producer; the native byte-endpoint exception does not apply to it.
	if out.Status != 141 || f.stdout.String() != "y\n" {
		t.Fatalf("status=%d stdout=%q", out.Status, f.stdout.String())
	}
}

func TestPipelineCancellationClosesEveryStream(t *testing.T) {
	f := newEvalFixture(t)
	f.log.started = make(chan string, 8)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	var out ProgramOutcome
	var err error
	go func() {
		out, err = f.run(t, ctx, "sleep 9 | cat | cat; echo never")
		close(done)
	}()
	for seen := 0; seen < 3; seen++ {
		select {
		case <-f.log.started:
		case <-time.After(5 * time.Second):
			t.Fatal("stages did not start")
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("canceled pipeline left a stage blocked")
	}
	if !errors.Is(err, context.Canceled) || out.Stop == nil || out.Stop.Kind != StopInterrupted {
		t.Fatalf("err=%v stop=%+v", err, out.Stop)
	}
	if len(f.log.calls) != 3 || slices.Contains(f.log.argv(), "echo never") {
		t.Fatalf("calls %q", f.log.argv())
	}
	// Each consumer's stream ends abnormally: by cancellation itself or by its
	// interrupted producer closing the pipe incomplete. Neither sees clean EOF.
	for _, call := range f.log.calls {
		if call.operation == "cat" && !errors.Is(call.err, context.Canceled) && !errors.Is(call.err, errStageIncomplete) {
			t.Fatalf("stage stream not closed by cancellation: %v", call.err)
		}
	}
	if out.Settlement("d").State != "interrupted" || f.log.forks != f.log.released {
		t.Fatalf("settlement %+v forks %d/%d", out.Settlement("d"), f.log.forks, f.log.released)
	}
}

func TestPipelineHostStopInOneStageStopsTheProgram(t *testing.T) {
	f := newEvalFixture(t)
	out, err := f.run(t, t.Context(), "echo a | rm -- x | cat; echo after")
	var coded *codedError
	if !errors.As(err, &coded) || out.Stop == nil || out.Stop.Kind != StopAuthority {
		t.Fatalf("err=%v stop=%+v", err, out.Stop)
	}
	if slices.Contains(f.log.argv(), "echo after") {
		t.Fatal("refusal in a stage was masked by later stages")
	}
}

// loopProgram reaches exactly n loop iterations: outer items times (inner+1)
// plus single-item loops for the remainder.
func loopProgram(n int64) string {
	const inner = 100
	outer := n / (inner + 1)
	rest := n - outer*(inner+1)
	var b strings.Builder
	b.WriteString("for a in" + strings.Repeat(" a", int(outer)) + "; do for b in" + strings.Repeat(" b", inner) + "; do x=1; done; done\n")
	for i := int64(0); i < rest; i++ {
		b.WriteString("for c in c; do x=1; done\n")
	}
	b.WriteString("echo finished\n")
	return b.String()
}

func TestLoopIterationCeilingAtBoundaryAndOnePast(t *testing.T) {
	limits, err := catalogEvaluationLimits()
	testutil.FailErr(t, "load evaluation limits", err)
	maximum := limits["loop_iterations"]
	f := newEvalFixture(t)
	if _, err := f.run(t, t.Context(), loopProgram(maximum)); err != nil || !slices.Equal(f.log.argv(), []string{"echo finished"}) {
		t.Fatalf("boundary refused: %v %q", err, f.log.argv())
	}
	f = newEvalFixture(t)
	out, err := f.run(t, t.Context(), loopProgram(maximum+1))
	assertLimit(t, err, "loop_iterations", maximum, maximum+1)
	if out.Stop == nil || len(f.log.calls) != 0 {
		t.Fatalf("one past the ceiling admitted work: %q", f.log.argv())
	}
}

func TestChildExecutionCounterChargeAtBoundaryAndOnePast(t *testing.T) {
	limits, err := catalogEvaluationLimits()
	if err != nil {
		t.Fatal(err)
	}
	maximum := limits["child_executions"]
	e := &evaluation{limits: limits, counters: map[string]int64{"child_executions": maximum - 1}}
	f := &frame{e: e, state: &frameState{latch: &OutcomeLatch{}}}
	loc := commandsurface.SyntaxLocation{}
	if err := e.charge(f, loc, "child_executions", 1); err != nil || e.counters["child_executions"] != maximum {
		t.Fatalf("at limit: counter=%d err=%v", e.counters["child_executions"], err)
	}
	err = e.charge(f, loc, "child_executions", 1)
	assertLimit(t, err, "child_executions", maximum, maximum+1)
	if e.counters["child_executions"] != maximum {
		t.Fatalf("over limit changed count to %d", e.counters["child_executions"])
	}
}

func TestEmptyInfiniteLoopTerminatesOnHostCounters(t *testing.T) {
	for program, binding := range map[string]string{
		"while x=1; do x=2; done; echo never":        "loop_iterations",
		"f() { while x=1; do continue; done; }; f":   "loop_iterations",
		"while :; do :; done; echo never":            "loop_iterations",
		"while echo a; do echo b; done; echo never":  "loop_iterations",
		"until false; do :; done; echo never":        "loop_iterations",
		"until [ -f ready ]; do :; done; echo never": "loop_iterations",
	} {
		f := newEvalFixture(t)
		out, err := f.run(t, t.Context(), program)
		var limit *ProgramLimitError
		if !errors.As(err, &limit) || limit.Limit != binding || out.Stop == nil {
			t.Fatalf("%q: %v", program, err)
		}
		if slices.Contains(f.log.argv(), "echo never") {
			t.Fatalf("%q admitted a child after exhaustion", program)
		}
	}
}

func TestFunctionRecursionCeilingAtBoundaryAndOnePast(t *testing.T) {
	limits, err := catalogEvaluationLimits()
	testutil.FailErr(t, "load evaluation limits", err)
	maximum := limits["function_depth"]
	// Depth d runs with d-1 arguments; recursion stops once depth reaches n.
	program := func(n int64) string {
		return fmt.Sprintf("f() { if test \"$#\" -lt %d; then f \"$@\" x; else echo deepest $#; fi; }; f", n-1)
	}
	f := newEvalFixture(t)
	if _, err := f.run(t, t.Context(), program(maximum)); err != nil || f.stdout.String() != fmt.Sprintf("deepest %d\n", maximum-1) {
		t.Fatalf("boundary depth refused: %v %q", err, f.stdout.String())
	}
	f = newEvalFixture(t)
	out, err := f.run(t, t.Context(), program(maximum+1))
	assertLimit(t, err, "function_depth", maximum, maximum+1)
	if out.Stop == nil || f.stdout.Len() != 0 {
		t.Fatal("recursion past the ceiling ran its body")
	}
	f = newEvalFixture(t)
	if _, err := f.run(t, t.Context(), "f() { f; }; f"); err == nil {
		t.Fatal("unbounded recursion was not refused")
	}
}

func TestStatementVisitCeiling(t *testing.T) {
	limits, err := catalogEvaluationLimits()
	testutil.FailErr(t, "load evaluation limits", err)
	// Each inner iteration visits 50 statements and stays well inside the
	// iteration ceiling, so visits are the binding counter.
	body := strings.Repeat("x=1; ", 50)
	program := "for a in" + strings.Repeat(" a", 50) + "; do for b in" + strings.Repeat(" b", 50) + "; do " + body + "done; done; echo never"
	f := newEvalFixture(t)
	_, err = f.run(t, t.Context(), program)
	assertLimit(t, err, "statement_visits", limits["statement_visits"], limits["statement_visits"]+1)
	if len(f.log.calls) != 0 {
		t.Fatal("work admitted after visit exhaustion")
	}
}

// assertLimit accepts the evaluator's own ceilings and expansion's, both
// latched as the same typed refusal.
func assertLimit(t *testing.T, err error, limit string, maximum, observed int64) {
	t.Helper()
	var refusal *EvaluationRefusal
	var program *ProgramLimitError
	var expansion *ExpansionError
	var got [3]any
	switch {
	case errors.As(err, &program):
		got = [3]any{program.Limit, program.Maximum, program.Observed}
	case errors.As(err, &expansion):
		got = [3]any{expansion.Limit, expansion.Maximum, expansion.Observed}
	}
	if !errors.As(err, &refusal) || refusal.Code != limitExceeded || got != [3]any{limit, maximum, observed} {
		t.Fatalf("want %s %d>%d refusal, got %v", limit, observed, maximum, err)
	}
}

func TestSourceRunsCapturedUnitInCurrentFrame(t *testing.T) {
	f := newEvalFixture(t)
	f.sources.files["./lib.sh"] = "greet() { echo hello $1; }\nshared=from-source\nreturn 0\necho unreachable\n"
	f.sources.changes["./lib.sh"] = "echo changed after capture\n"
	out, err := f.run(t, t.Context(), "echo before; source -- ./lib.sh; greet $shared; source -- ./lib.sh; echo $?")
	testutil.FailErr(t, "run evaluated command program", err)
	if got := f.log.argv(); !slices.Equal(got, []string{"echo before", "echo hello from-source", "echo changed after capture", "echo 0"}) {
		t.Fatalf("dispatched %q", got)
	}
	if len(out.Sources) != 2 || out.Sources[0].Pin.SHA256 == out.Sources[1].Pin.SHA256 || out.Sources[0].Pin.SourceID != "./lib.sh" {
		t.Fatalf("source identities %+v", out.Sources)
	}
	if f.log.find(t, "echo").sourceUnit != "program" {
		t.Fatal("parent statement attributed to sourced unit")
	}
	for _, call := range f.log.calls {
		if call.argv[1] == "changed" && call.sourceUnit != "./lib.sh" {
			t.Fatalf("sourced child attributed to %q", call.sourceUnit)
		}
	}
}

// Q10: a sourced unit with excluded syntax runs nothing, and earlier parent
// work stays recorded as executed rather than replayable.
func TestSourceSyntaxRefusalKeepsEarlierParentEffects(t *testing.T) {
	for name, script := range map[string]string{"excluded": "echo inner\ntrap x INT\n", "invalid": "echo inner\nif then\n", "bom": "\xef\xbb\xbfecho inner\n"} {
		t.Run(name, func(t *testing.T) {
			f := newEvalFixture(t)
			f.sources.files["./bad.sh"] = script
			out, err := f.run(t, t.Context(), "echo parent; source -- ./bad.sh; echo after")
			var refusal *EvaluationRefusal
			if !errors.As(err, &refusal) || !strings.HasPrefix(refusal.Code, "COMMAND_") || out.Stop == nil {
				t.Fatalf("refusal %v", err)
			}
			if got := f.log.argv(); !slices.Equal(got, []string{"echo parent"}) {
				t.Fatalf("dispatched %q", got)
			}
			if len(out.Sources) != 1 || out.Children[0].Command != "echo" || !out.Children[0].Executed {
				t.Fatalf("earlier parent effect or source identity lost: %+v", out.Children)
			}
		})
	}
}

func TestSourceUsageAndDepth(t *testing.T) {
	f := newEvalFixture(t)
	if _, err := f.run(t, t.Context(), "source -x; echo $?"); err != nil || f.stdout.String() != "2\n" {
		t.Fatalf("an option operand was not usage: %v %q", err, f.stdout.String())
	}
	f = newEvalFixture(t)
	f.sources.files["./lib.sh"] = "echo sourced\n"
	if _, err := f.run(t, t.Context(), "source ./lib.sh; echo $?"); err != nil || !strings.HasSuffix(f.stdout.String(), "0\n") {
		t.Fatalf("plain source path: %v %q", err, f.stdout.String())
	}
	limits, _ := catalogEvaluationLimits()
	f = newEvalFixture(t)
	f.sources.files["./self.sh"] = "echo level\nsource -- ./self.sh\n"
	_, err := f.run(t, t.Context(), "source -- ./self.sh")
	assertLimit(t, err, "source_depth", limits["source_depth"], limits["source_depth"]+1)
	if int64(len(f.log.calls)) != limits["source_depth"] {
		t.Fatalf("levels %d", len(f.log.calls))
	}
	f = newEvalFixture(t)
	if _, err := f.run(t, t.Context(), "source -- ./missing.sh; echo $?"); err != nil || f.stdout.String() != "1\n" {
		t.Fatalf("missing script %v %q", err, f.stdout.String())
	}
}

// Q16 (evaluator part): the job keeps the cwd and environment captured when
// it was started; later parent changes never reach it.
func TestBackgroundJobRunsWithCapturedState(t *testing.T) {
	f := newEvalFixture(t)
	out, err := f.run(t, t.Context(), "cd a; export X=1; { env; pwd; } &\necho $!; cd b; X=2")
	if err != nil || out.Status != 0 {
		t.Fatalf("evaluate: %v", err)
	}
	if f.stdout.String() != "job-1\n" || !slices.Equal(out.Jobs, []string{"job-1"}) || len(f.jobs.runs) != 1 {
		t.Fatalf("job handle %q %v", f.stdout.String(), out.Jobs)
	}
	var jobOut bytes.Buffer
	result, err := f.jobs.runs[0](t.Context(), Streams{Output: &jobOut})
	if err != nil || result.Status != 0 || jobOut.String() != "X=1\n/a\n" {
		t.Fatalf("job ran with mutated state: %v %+v %q", err, result, jobOut.String())
	}
	if f.log.forks != f.log.released {
		t.Fatalf("job directory leaked: %d/%d", f.log.forks, f.log.released)
	}
}

func TestSubstitutionBounds(t *testing.T) {
	limits, _ := catalogEvaluationLimits()
	f := newEvalFixture(t)
	nested := "echo x"
	for i := int64(0); i <= limits["substitution_depth"]; i++ {
		nested = "echo $(" + nested + ")"
	}
	_, err := f.run(t, t.Context(), nested)
	assertLimit(t, err, "substitution_depth", limits["substitution_depth"], limits["substitution_depth"]+1)
	f = newEvalFixture(t)
	f.ports.Input = fakeInput{"big": strings.Repeat("z", int(limits["substitution_bytes"])+1)}
	_, err = f.run(t, t.Context(), "x=$(cat < big); echo never")
	assertLimit(t, err, "substitution_bytes", limits["substitution_bytes"], limits["substitution_bytes"]+1)
	if slices.Contains(f.log.argv(), "echo never") {
		t.Fatal("oversized substitution admitted later work")
	}
}

func TestEvaluateRejectsMismatchedPlanAndPorts(t *testing.T) {
	f := newEvalFixture(t)
	f.ports.Dispatch = nil
	if _, err := f.run(t, t.Context(), "echo a"); err == nil {
		t.Fatal("evaluated without dispatch")
	}
	f = newEvalFixture(t)
	f.ports.Invocation.CatalogDigest = "other"
	if _, err := f.run(t, t.Context(), "echo a"); err == nil || len(f.log.calls) != 0 {
		t.Fatal("evaluated under a different catalog")
	}
	f = newEvalFixture(t)
	f.ports.Invocation.Kind = toolcontract.StructuredToolInvocation
	if _, err := f.run(t, t.Context(), "echo a"); err == nil {
		t.Fatal("evaluated under a structured invocation")
	}
}

// compoundCloseDispatch keeps the byte endpoint's close plus the utility's
// original diagnostic; the evaluator must not replace it with a bare close.
type compoundCloseDispatch struct {
	Dispatch
	joined  bool
	failure error
}

func (d compoundCloseDispatch) Fork(ctx context.Context) (Dispatch, func(), error) {
	child, release, err := d.Dispatch.Fork(ctx)
	return compoundCloseDispatch{Dispatch: child, joined: d.joined, failure: d.failure}, release, err
}
func (d compoundCloseDispatch) Invoke(ctx context.Context, request OperationRequest, streams Streams) (toolcontract.ProducerResult, error) {
	if request.Argv[0].Value != "cat" {
		return d.Dispatch.Invoke(ctx, request, streams)
	}
	_, err := io.WriteString(streams.Output, "one\n")
	if err == nil {
		_, err = io.WriteString(streams.Output, "two\n")
	}
	result := toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.NoEffect}
	if errors.Is(err, ErrConsumerClosed) {
		if d.joined {
			err = errors.Join(err, d.failure)
		} else {
			err = fmt.Errorf("synthetic utility diagnostic: %w", err)
		}
	}
	return result, err
}
func TestPipelineWrappedCloseCannotPublish(t *testing.T) {
	for _, joined := range []bool{false, true} {
		t.Run(fmt.Sprint(joined), func(t *testing.T) {
			f := newEvalFixture(t)
			failure := errors.New("independent utility failure")
			f.ports.Dispatch = compoundCloseDispatch{Dispatch: f.ports.Dispatch, joined: joined, failure: failure}
			out, err := f.run(t, t.Context(), "echo kept > earlier; { cat <<EOF | head -n 1; } > result; echo $?\none\ntwo\nthree\nEOF\n")
			if err != nil || out.Stop != nil || f.stdout.String() != "1\n" || f.publication.published["earlier"] != "kept\n" || f.publication.published["result"] != "" || !slices.Contains(f.publication.abandoned, "result") {
				t.Fatalf("joined=%v outcome=%+v err=%v published=%v abandoned=%v calls=%v", joined, out, err, f.publication.published, f.publication.abandoned, f.log.argv())
			}
			found := false
			for _, child := range out.Children {
				if child.Command != "cat" {
					continue
				}
				found = true
				if child.Result.Status != 1 || child.Result.Complete || child.Result.PublicationEligible || !errors.Is(child.Diagnostic, ErrConsumerClosed) || (joined && !errors.Is(child.Diagnostic, failure)) {
					t.Fatalf("lost producer diagnostic or failed settlement: %+v", child)
				}
			}
			if !found {
				t.Fatal("missing producer settlement")
			}

		})
	}
}
