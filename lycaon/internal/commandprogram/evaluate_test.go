package commandprogram

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestEvaluateStatusAndShortCircuitOrdering(t *testing.T) {
	cases := []struct {
		name     string
		program  string
		dispatch []string
		status   int
		exited   bool
	}{
		{"sequence continues after ordinary failure", "grep z; echo after $?", []string{"grep z", "echo after 1"}, 0, false},
		{"and skips after failure", "grep z && echo yes", []string{"grep z"}, 1, false},
		{"or runs after failure", "grep z && echo yes || echo no", []string{"grep z", "echo no"}, 0, false},
		{"or skips after success", "echo a || echo b", []string{"echo a"}, 0, false},
		{"negation inverts ordinary status", "! grep z; echo $?", []string{"grep z", "echo 0"}, 0, false},
		{"negated compound", "! { grep z; }; echo $?", []string{"grep z", "echo 0"}, 0, false},
		{"builtins", "false; echo $?; true; echo $?; : anything -x; echo $?", []string{"echo 1", "echo 0", "echo 0"}, 0, false},
		{"elif chain", "if grep z; then echo t; elif [ a = a ]; then echo e; else echo f; fi", []string{"grep z", "echo e"}, 0, false},
		{"if without taken branch is zero", "if grep z; then echo t; fi; echo $?", []string{"grep z", "echo 0"}, 0, false},
		{"while false never runs body", "while false; do echo x; done; echo $?", []string{"echo 0"}, 0, false},
		{"for items", "for i in a 'b c'; do echo $i; done", []string{"echo a", "echo b c"}, 0, false},
		{"for positional with no arguments", "for i; do echo $i; done; echo done", []string{"echo done"}, 0, false},
		{"continue", "for i in a b c; do if [ $i = b ]; then continue; fi; echo $i; done", []string{"echo a", "echo c"}, 0, false},
		{"break two levels", "for i in a b; do for j in x y; do echo $i$j; break 2; done; done; echo $?", []string{"echo ax", "echo 0"}, 0, false},
		{"break outside loop is usage", "break; echo $?", []string{"echo 2"}, 0, false},
		{"function arguments are frame local", "f() { echo $1 $#; return 3; echo no; }; f z y; echo $? $1", []string{"echo z 2", "echo 3"}, 0, false},
		{"return outside function is usage", "return 1; echo $?", []string{"echo 2"}, 0, false},
		{"exit stops the program", "echo a; exit 4; echo b", []string{"echo a"}, 4, true},
		{"exit without operand keeps status", "grep z; exit", []string{"grep z"}, 1, true},
		{"exit out of range is usage", "exit 256; echo $?", []string{"echo 2"}, 0, false},
		{"exit in function ends program", "f() { exit 5; }; f; echo no", nil, 5, true},
		{"isolated exit leaves only the group", "( exit 3; echo no ); echo $?", []string{"echo 3"}, 0, false},
		{"isolated variables", "x=1; ( x=2; echo $x ); echo $x", []string{"echo 2", "echo 1"}, 0, false},
		{"braces share variables", "x=1; { x=2; }; echo $x", []string{"echo 2"}, 0, false},
		{"unset", "x=1; unset x; echo [$x]", []string{"echo []"}, 0, false},
		{"substitution", "echo $(echo inner) $(echo two)", []string{"echo inner", "echo two", "echo inner two"}, 0, false},
		{"failed substitution prevents containing dispatch", "echo $(grep z) never; echo $?", []string{"grep z", "echo 1"}, 0, false},
		{"failed substitution in assignment", "x=$(grep z); echo $? [$x]", []string{"grep z", "echo 1 []"}, 0, false},
		{"substitution is isolated", "x=1; echo $(x=2; echo $x) $x", []string{"echo 2", "echo 2 1"}, 0, false},
		{"pipeline rightmost nonzero", "echo a | grep z; echo $?", []string{"echo a", "grep z", "echo 1"}, 0, false},
		{"errexit exits on failure", "set -e; echo a; grep z; echo no", []string{"echo a", "grep z"}, 1, true},
		{"errexit ignores conditions and non-final lists", "set -e; if grep z; then echo no; fi; grep z || echo or; grep z && echo no; ! grep z; echo end", []string{"grep z", "grep z", "echo or", "grep z", "grep z", "echo end"}, 0, false},
		{"errexit judges a list by its final command", "set -euo pipefail; echo a && grep z; echo no", []string{"echo a", "grep z"}, 1, true},
		{"errexit applies to a failed pipeline", "set -e; echo a | grep z; echo no", []string{"echo a", "grep z"}, 1, true},
		{"errexit exits from a failed subshell", "set -e; ( grep z; echo no ); echo no", []string{"grep z"}, 1, true},
		{"errexit is off inside substitutions", "set -e; x=$(grep z; echo in); echo $x", []string{"grep z", "echo in", "echo in"}, 0, false},
		{"set +e turns errexit off", "set -e; set +e; grep z; echo after", []string{"grep z", "echo after"}, 0, false},
		{"set usage", "set -e -o; echo $?", []string{"echo 2"}, 0, false},
		{"case runs the first match", "x=main.go; case $x in *.rs) echo rust;; *.go|*.c) echo go;; *) echo other;; esac", []string{"echo go"}, 0, false},
		{"case without a match is status 0", "case x in y) echo no;; esac; echo $?", []string{"echo 0"}, 0, false},
		{"case quoted pattern is literal", "x='*'; case a in \"$x\") echo no;; a) echo a;; esac", []string{"echo a"}, 0, false},
		{"arithmetic drives a loop", "i=0; while [ $i -lt 3 ]; do echo $i; i=$((i + 1)); done", []string{"echo 0", "echo 1", "echo 2"}, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newEvalFixture(t)
			out, err := f.run(t, t.Context(), c.program)
			if err != nil {
				t.Fatalf("evaluate: %v", err)
			}
			got := f.log.argv()
			if !slices.Equal(got, c.dispatch) && !(len(got) == 0 && len(c.dispatch) == 0) {
				t.Fatalf("dispatched %q, want %q", got, c.dispatch)
			}
			if out.Status != c.status || out.Exited != c.exited || out.Stop != nil {
				t.Fatalf("outcome status=%d exited=%t stop=%+v", out.Status, out.Exited, out.Stop)
			}
			if f.log.forks != f.log.released {
				t.Fatalf("forked directories leaked: forks=%d released=%d", f.log.forks, f.log.released)
			}
		})
	}
}

func TestEvaluateNounsetRefusesUnsetParameters(t *testing.T) {
	for _, program := range []string{"set -u; echo ${UNSET:-ok}; echo $UNSET; echo no", "set -o nounset; echo ${UNSET:-ok}; echo $1; echo no"} {
		f := newEvalFixture(t)
		out, err := f.run(t, t.Context(), program)
		if got := f.log.argv(); !slices.Equal(got, []string{"echo ok"}) {
			t.Fatalf("%s dispatched %q", program, got)
		}
		if err == nil && out.Stop == nil {
			t.Fatalf("%s ran past an unbound parameter: %+v", program, out)
		}
	}
}

func TestEvaluateEnvironmentIsInvocationLocal(t *testing.T) {
	f := newEvalFixture(t)
	if _, err := f.run(t, t.Context(), "export A=1; B=2; FOO=bar env; env; export B; env"); err != nil {
		testutil.FailErr(t, "run evaluated command program", err)
	}
	if got := f.stdout.String(); got != "A=1\nFOO=bar\nA=1\nA=1\nB=2\n" {
		t.Fatalf("environment %q", got)
	}
}

func TestEvaluateChildIdentitiesAreUniquePerExecution(t *testing.T) {
	f := newEvalFixture(t)
	out, err := f.run(t, t.Context(), "for i in a b c; do echo $i; done")
	testutil.FailErr(t, "run evaluated command program", err)
	seen := map[string]bool{}
	var node string
	for _, call := range f.log.calls {
		if seen[call.execution.ExecutionID] || call.execution.ParentExecutionID != f.ports.Invocation.Identity.ExecutionID || call.sourceUnit != "program" {
			t.Fatalf("iteration identity reused or misparented: %+v", call.execution)
		}
		seen[call.execution.ExecutionID] = true
		if node != "" && call.execution.NodeID != node {
			t.Fatal("iterations did not retain their static node")
		}
		node = call.execution.NodeID
	}
	if len(seen) != 3 || len(out.Children) < 3 {
		t.Fatalf("children %d", len(out.Children))
	}
}

func TestEvaluateOwnerRefusalLatchesAndKeepsOwnerCode(t *testing.T) {
	f := newEvalFixture(t)
	out, err := f.run(t, t.Context(), "echo before; rm -- x || echo handled; echo after")
	var coded *codedError
	if !errors.As(err, &coded) || coded.code != "TOOL_NOT_OFFERED" {
		t.Fatalf("owner rejection lost: %v", err)
	}
	if out.Stop == nil || out.Stop.Kind != StopAuthority || out.Stop.Result.StopCode != "TOOL_NOT_OFFERED" {
		t.Fatalf("stop %+v", out.Stop)
	}
	if got := f.log.argv(); !slices.Equal(got, []string{"echo before", "rm -- x"}) {
		t.Fatalf("admitted after refusal: %q", got)
	}
	settlement := out.Settlement("detail")
	if settlement.State != "rejected" || settlement.StopCode != "TOOL_NOT_OFFERED" || settlement.Validate() != nil {
		t.Fatalf("settlement %+v", settlement)
	}
}

func TestEvaluateUnsupportedNativeOptionRefusesWithoutFallback(t *testing.T) {
	f := newEvalFixture(t)
	out, err := f.run(t, t.Context(), "echo before; cat --not-a-version-1-option; echo after")
	var refusal *EvaluationRefusal
	if !errors.As(err, &refusal) || refusal.Code != "COMMAND_OPTION_UNSUPPORTED" || out.Stop == nil {
		t.Fatalf("refusal=%v stop=%+v", err, out.Stop)
	}
	if got := f.log.argv(); !slices.Equal(got, []string{"echo before"}) {
		t.Fatalf("unsupported form dispatched or fell back: %q", got)
	}
}

func TestEvaluateRefusesExcludedConstructsBeforeAnyEffect(t *testing.T) {
	cases := []struct {
		name    string
		program string
		remove  func(*EvaluationPorts)
	}{
		{"input redirect without owner", "echo a; cat < input.txt", func(p *EvaluationPorts) { p.Input = nil }},
		{"output redirect without owner", "echo a; echo b > out", func(p *EvaluationPorts) { p.Publication = nil }},
		{"background without supervision", "echo a; sleep 1 &", func(p *EvaluationPorts) { p.Supervision = nil }},
		{"source without capture", "echo a; source -- ./s.sh", func(p *EvaluationPorts) { p.Sources = nil }},
		{"reserved function name", "echo a; exit() { echo b; }", func(*EvaluationPorts) {}},
		{"nested in substitution", "echo a; echo $(echo b > out)", func(p *EvaluationPorts) { p.Publication = nil }},
		{"output on stdin descriptor", "echo a; echo b 0> out", func(*EvaluationPorts) {}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newEvalFixture(t)
			c.remove(&f.ports)
			out, err := f.run(t, t.Context(), c.program)
			var refusal *EvaluationRefusal
			if !errors.As(err, &refusal) || refusal.Code != "COMMAND_SYNTAX_UNSUPPORTED" || refusal.MachineErrorCode() != refusal.Code {
				t.Fatalf("refusal %v", err)
			}
			if len(f.log.calls) != 0 || len(out.Children) != 0 {
				t.Fatalf("effects before refusal: %q", f.log.argv())
			}
		})
	}
}

func TestEvaluateRedirectsPublishOnlyEligibleOutput(t *testing.T) {
	f := newEvalFixture(t)
	out, err := f.run(t, t.Context(), "echo a > out; echo b >> out; grep z > nomatch; fail 1 > none; echo c 2>&1 > both; cat < input.txt > copy; echo d > denied; echo $?")
	testutil.FailErr(t, "run evaluated command program", err)
	published := f.publication.published
	// Native comparison status 1 is complete and publishable; unclassified
	// external status 1 is not.
	if published["out"] != "a\nb\n" || published["both"] != "c\n" || published["copy"] != "from file\n" || published["nomatch"] != "" {
		t.Fatalf("published %q", published)
	}
	if _, ok := published["none"]; ok || !slices.Equal(f.publication.abandoned, []string{"none"}) || !slices.Contains(sortedPublished(published), "nomatch") {
		t.Fatalf("ineligible output published: %q abandoned %q", published, f.publication.abandoned)
	}
	if f.stdout.String() != "1\n" || out.Status != 0 {
		t.Fatalf("refused open did not leave status 1: %q", f.stdout.String())
	}
}

func TestEvaluateHereDocumentIsStdin(t *testing.T) {
	f := newEvalFixture(t)
	program := "x=value\ncat <<EOF\nline $x\nEOF\ncat <<'EOF'\nraw $x\nEOF\n"
	if _, err := f.run(t, t.Context(), program); err != nil {
		testutil.FailErr(t, "run evaluated command program", err)
	}
	if got := f.stdout.String(); got != "line value\nraw $x\n" {
		t.Fatalf("here-document stdin %q", got)
	}
}

func TestEvaluateSettlementStates(t *testing.T) {
	for _, c := range []struct {
		stop  *HostStop
		state string
	}{
		{nil, "completed"},
		{&HostStop{Kind: StopInterrupted}, "interrupted"},
		{&HostStop{Kind: StopOwner, Result: toolcontract.ProducerResult{StopCode: limitExceeded}}, "failed"},
	} {
		if got := (ProgramOutcome{Status: 3, Stop: c.stop}).Settlement("d"); got.State != c.state || *got.Status != 3 {
			t.Fatalf("settlement %+v", got)
		}
	}
	if !strings.Contains((&EvaluationRefusal{Code: "X", Expected: "y", Cause: errors.New("z")}).Error(), "z") {
		t.Fatal("refusal lost its cause")
	}
}

func sortedPublished(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func TestUntakenBranchesNeverExpand(t *testing.T) {
	f := newEvalFixture(t)
	program := "if grep z; then echo $(rm -- never); fi; echo a || echo $(rm -- never); grep z && echo $(rm -- never); while false; do echo $(rm -- never); done; echo done"
	out, err := f.run(t, t.Context(), program)
	if err != nil || out.Stop != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := f.log.argv(); !slices.Equal(got, []string{"grep z", "echo a", "grep z", "echo done"}) {
		t.Fatalf("untaken word expanded: %q", got)
	}
	for _, child := range out.Children {
		if child.Command == "$()" {
			t.Fatal("untaken substitution left a child execution")
		}
	}
}

func TestFailedSubstitutionNeverLaunchesItsCommand(t *testing.T) {
	f := newEvalFixture(t)
	out, err := f.run(t, t.Context(), "echo before; echo $(fail 3) never; echo status $?; x=$(fail 4); echo assigned $?")
	testutil.FailErr(t, "run evaluated command program", err)
	if got := f.log.argv(); !slices.Equal(got, []string{"echo before", "fail 3", "echo status 3", "fail 4", "echo assigned 4"}) {
		t.Fatalf("dispatched %q", got)
	}
	if !out.Children[0].Executed {
		t.Fatal("earlier effect lost")
	}
	f = newEvalFixture(t)
	out, err = f.run(t, t.Context(), "echo before; echo $(rm -- x) never || echo handled")
	var coded *codedError
	if !errors.As(err, &coded) || out.Stop == nil || out.Stop.Kind != StopAuthority {
		t.Fatalf("host stop in substitution: %v %+v", err, out.Stop)
	}
	if got := f.log.argv(); !slices.Equal(got, []string{"echo before", "rm -- x"}) {
		t.Fatalf("command launched after refused substitution: %q", got)
	}
}

// Q08: a selector read through substitution chooses the operation as data;
// the allowed target runs, the forbidden one is refused before any mutation,
// and the selector read stays recorded. Metacharacters never become syntax.
func TestDynamicSelectorBindsOneOperationAsData(t *testing.T) {
	for _, c := range []struct {
		selector string
		dispatch []string
		refused  bool
	}{
		{"cat", []string{"cat selector", "cat input"}, false},
		{"rm", []string{"cat selector", "rm input"}, true},
		{"echo a; rm -- input", []string{"cat selector", "echo a; rm -- input input"}, false},
	} {
		t.Run(c.selector, func(t *testing.T) {
			f := newEvalFixture(t)
			f.log.files = map[string]string{"selector": c.selector + "\n", "input": "payload\n"}
			out, err := f.run(t, t.Context(), "cmd=$(cat selector); \"$cmd\" input")
			if got := f.log.argv(); !slices.Equal(got, c.dispatch) {
				t.Fatalf("dispatched %q", got)
			}
			if c.refused != (out.Stop != nil && out.Stop.Kind == StopAuthority) || c.refused != (err != nil) {
				t.Fatalf("refusal=%v stop=%+v", err, out.Stop)
			}
			if !c.refused && c.selector == "cat" && f.stdout.String() != "payload\n" {
				t.Fatalf("allowed target output %q", f.stdout.String())
			}
			if out.Children[0].Command != "cat" || !out.Children[0].Executed || out.Children[0].Result.Status != 0 {
				t.Fatalf("selector read lost: %+v", out.Children[0])
			}
		})
	}
}
