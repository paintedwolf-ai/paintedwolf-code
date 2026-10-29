package toolhost

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/lycaon/lycaon/internal/gitengine"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools/native/command"
)

func callComposedProgram(t *testing.T, f *programtest.Fixture, source string) command.ProgramOutcome {
	t.Helper()
	raw, err := f.Call(t, t.Context(), source)
	testutil.FailErr(t, "run composed program", err)
	var outcome command.ProgramOutcome
	testutil.FailErr(t, "decode program outcome", json.Unmarshal([]byte(raw), &outcome))
	return outcome
}

// Discovery answers a native name with the name dispatch binds, so a printed
// name runs natively; an absolute path still launches the installed program.
func TestComposedProgramDiscoveryAgreesWithDispatch(t *testing.T) {
	bin, err := gitengine.BinaryPath()
	testutil.FailErr(t, "require pinned Git", err)
	// Both discovery and launch select a declared executable without relying on
	// an ambient Git installation. The native Git owner still uses its bundle.
	t.Setenv("PATH", filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"))
	f := newComposedProgramFixture(t)
	setup := exec.Command(bin, "init", "-q", "-b", "main", f.Dir)
	setup.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	if output, err := setup.CombinedOutput(); err != nil {
		t.Fatalf("initialize fixture with pinned Git: %v: %s", err, output)
	}
	outcome := callComposedProgram(t, f, `which git; command -v git; type git; command -V git; git status --porcelain`)
	want := "git\ngit\ngit is a native built-in\ngit is a native built-in\n"
	if outcome.Stdout != want || outcome.Stderr != "" || outcome.Status != 0 || outcome.Refusal != nil {
		t.Fatalf("discovery outcome: %+v", outcome)
	}
	if got := f.Executions(t)["git status"]; got != "completed;" {
		t.Fatalf("native Git receipts: %q", got)
	}
	f.Runtime.ExternalHidden = true
	outcome = callComposedProgram(t, f, `BIN=$(which git); "$BIN" status --porcelain; printf 'status=%d\n' "$?"`)
	if outcome.Stdout != "status=0\n" || outcome.Stderr != "" || outcome.Status != 0 || outcome.Refusal != nil {
		t.Fatalf("printed name outcome: %+v", outcome)
	}
	if got := f.Executions(t)["git status"]; got != "completed;completed;" {
		t.Fatalf("printed name did not run native Git: %q", got)
	}
	outcome = callComposedProgram(t, f, strconv.Quote(bin)+` status --porcelain; printf 'status=%d\n' "$?"`)
	if outcome.Stdout != "status=0\n" || outcome.Stderr != "" || outcome.Status != 0 || outcome.Refusal != nil {
		t.Fatalf("absolute path launch outcome: %+v", outcome)
	}
	var launched int
	testutil.FailErr(t, "read external launch receipt", f.Database.QueryRowContext(t.Context(), `SELECT count(*) FROM invocation_program_executions WHERE program_id=? AND operation_id='command' AND native_command='external' AND state='completed' AND owner_invoked=1`, outcome.ProgramID).Scan(&launched))
	if launched != 1 {
		t.Fatalf("absolute path external launch receipts: %d", launched)
	}
	outcome = callComposedProgram(t, f, "external -- "+strconv.Quote(bin)+" status --porcelain")
	if outcome.Refusal == nil || outcome.Refusal.Code != "COMMAND_EXTERNAL_HIDDEN" || outcome.Stdout != "" {
		t.Fatalf("explicit hidden selector outcome: %+v", outcome)
	}
}

func TestComposedProgramIdiomsHaveExactBytesAndStatuses(t *testing.T) {
	cases := []struct {
		name, source, stdout, stderr string
		status                       int
	}{
		{"replacement and trimming", `s=a/b/c; printf '%s\n' "${s//\//-}" "${s#*/}" "${s%/*}"`, "a-b-c\nb/c\na/b\n", "", 0},
		{"anchored replacement", `s=abc; e=; printf '%s\n' "${s/#a/x}" "${s/%c/y}" "${e/#*/x}" "${e/%*/y}"`, "xbc\naby\nx\ny\n", "", 0},
		{"shortest and longest trimming", `s=abcbcd; printf '%s\n' "${s#a*c}" "${s##a*c}" "${s%b*d}" "${s%%b*d}"`, "bcd\nd\nabc\na\n", "", 0},
		{"Unicode case", `s=äBc; printf '%s\n' "${s^}" "${s^^}" "${s,}" "${s,,}"`, "ÄBc\nÄBC\näBc\näbc\n", "", 0},
		{"unicode slices", `s='aβ🙂z'; printf '%s\n' "${s:1:2}" "${s: -1}" "${s:1:-1}"`, "β🙂\nz\nβ🙂\n", "", 0},
		{"arrays", `a=(first 'second third'); a+=(fourth); a[4]=fifth; a[0]+=!; printf '%s\n' "${a[@]}" "${#a[@]}" "${a[@]:1:2}"`, "first!\nsecond third\nfourth\nfifth\n4\nsecond third\nfourth\n", "", 0},
		{"scalar assignment preserves array", `a=(one two); a=three; printf '%s\n' "${a[@]}"; ((a=4)); printf '%s\n' "${a[@]}"; a+=5; printf '%s\n' "${a[@]}"`, "three\ntwo\n4\ntwo\n45\ntwo\n", "", 0},
		{"array export", `a=(one two); export a; printenv a; printf 'status=%d\n' "$?"`, "status=1\n", "", 0},
		{"read array element zero", `a=(one two); read -r a < input.txt; printf '%s\n' "${a[@]}"`, "from file\ntwo\n", "", 0},
		{"arithmetic", `((i=0)); while ((i<3)); do printf '%d\n' "$i"; ((i++)); done; ((0)); printf 'status=%d\n' "$?"`, "0\n1\n2\nstatus=1\n", "", 0},
		{"arithmetic indexed read", `a=(10 20); ((i=0)); ((v=a[i++]+1)); printf '%s:%s\n' "$v" "$i"`, "11:1\n", "", 0},
		{"arithmetic assignment order", `((i=0)); ((a[i++]=i)); printf '%s:%s\n' "${a[0]}" "$i"; ((v=1,v+=(v=2))); printf '%s\n' "$v"; ((i=0)); ((b[i]=i++)); printf '%s:%s\n' "${b[1]}" "$i"`, "0:1\n3\n0:1\n", "", 0},
		{"short circuit arithmetic", `((a=0)); ((0 && a++)); ((1 || a++)); printf '%d\n' "$a"`, "0\n", "", 0},
		{"trailing duplication", `printf '%s\n' error >&2`, "", "error\n", 0},
		{"leading duplication", `>&2 printf '%s\n' error`, "", "error\n", 0},
		{"middle duplication", `printf >&2 '%s\n' error`, "", "error\n", 0},
		{"pipeline duplication", `printf 'err\n' 2>&1 1>&2 | cat`, "err\n", "", 0},
		{"general descriptor swap", `{ printf 'out\n'; printf 'err\n' >&2; } 9>&1 1>&2 2>&9`, "err\n", "out\n", 0},
		{"custom descriptor read and write", `{ printf 'line1\nline2\n' >&4; } 4> fd4.txt; { read -r x <&3; read -r y <&3; printf '%s+%s\n' "$x" "$y"; } 3< fd4.txt`, "line1+line2\n", "", 0},
		{"trap status", `trap 'printf "exit=%d\n" "$?"' EXIT; printf 'body\n'; exit 7`, "body\nexit=7\n", "", 7},
		{"trap replacement", `trap 'printf "wrong\n"' EXIT; trap 'printf "right\n"' EXIT`, "right\n", "", 0},
		{"trap explicit exit", `trap 'printf "trap\n"; exit 3' EXIT; exit 7`, "trap\n", "", 3},
		{"trap clear", `trap 'printf "wrong\n"' EXIT; trap - EXIT; printf 'body\n'`, "body\n", "", 0},
		{"subshell local trap", `(trap 'printf "sub\n"' EXIT; printf 'body\n'); printf 'parent\n'`, "body\nsub\nparent\n", "", 0},
		{"pipeline stage local trap", `(trap 'printf "sub\n"' EXIT; printf 'body\n') | cat`, "body\nsub\n", "", 0},
		{"command substitution local trap", `s=$(trap 'printf "sub\n"' EXIT; printf 'body\n'); printf '%s\n' "$s"`, "body\nsub\n", "", 0},
		{"diff process substitutions", `diff -u <(printf 'b\na\n' | sort) <(printf 'a\nb\n' | sort)`, "", "", 0},
		{"comm process substitutions", `comm -12 <(printf 'c\na\nb\n' | sort) <(printf 'b\nd\na\n' | sort)`, "a\nb\n", "", 0},
		{"paste process substitutions", `paste <(printf '1\n2\n') <(printf 'a\nb\n')`, "1\ta\n2\tb\n", "", 0},
		{"early consumer exit on process substitution", `head -n 1 <(while true; do printf 'y\n'; done)`, "y\n", "", 0},
		{"input redirect from process substitution", `read -r line < <(printf 'streamed-in\n'); printf '%s\n' "$line"`, "streamed-in\n", "", 0},
		{"output process substitution via tee and redirect", `printf 'hello\n' | tee >(tr a-z A-Z > upper.txt) > lower.txt; printf '%s:%s\n' "$(cat lower.txt)" "$(cat upper.txt)"`, "hello:HELLO\n", "", 0},
		{"direct redirect to output process substitution", `printf 'world\n' > >(tr a-z A-Z > direct.txt); cat direct.txt`, "WORLD\n", "", 0},
		{"native grep embedded --file=<(...) process substitution", `grep --file=<(printf 'needle\n') <(printf 'hay\nneedle\n')`, "needle\n", "", 0},
		{"native sed attached -f<(...) process substitution", `sed -f<(printf 's/world/earth/\n') <(printf 'hello world\n')`, "hello earth\n", "", 0},
		{"native grep literal /dev/fd/63 and /dev/fd/6 next to <(...)", `grep -F -e /dev/fd/63 -e /dev/fd/6 <(printf '/dev/fd/63\n/dev/fd/6\nother\n')`, "/dev/fd/63\n/dev/fd/6\n", "", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newComposedProgramFixture(t)
			testutil.FailErr(t, "seed read input", os.WriteFile(filepath.Join(f.Dir, "input.txt"), []byte("from file\n"), 0o600))
			outcome := callComposedProgram(t, f, c.source)
			if outcome.Stdout != c.stdout || outcome.Stderr != c.stderr || outcome.Status != c.status || outcome.Refusal != nil {
				t.Fatalf("outcome %+v; want stdout=%q stderr=%q status=%d", outcome, c.stdout, c.stderr, c.status)
			}
		})
	}
}

func TestComposedProgramArrayOverflowRendersTypedRefusal(t *testing.T) {
	f := newComposedProgramFixture(t)
	outcome := callComposedProgram(t, f, `printf 'before\n'; a[9223372036854775807]=last; a+=(more); printf 'unreached\n'`)
	if outcome.Refusal == nil || outcome.Refusal.Code != "COMMAND_OPERAND_INVALID" || outcome.Refusal.Feedback == "" || outcome.Status != 2 || outcome.Stdout != "before\n" {
		t.Fatalf("array overflow outcome: %+v", outcome)
	}
	t.Logf("Rendered array overflow refusal:\n%s", outcome.Refusal.Feedback)
}

func TestComposedProgramProcessSubstitutionWithExternalAndEarlyExit(t *testing.T) {
	f := newComposedProgramFixture(t)
	if _, err := exec.LookPath("yes"); err == nil {
		outcome := callComposedProgram(t, f, `head -n 1 <(yes)`)
		if outcome.Stdout != "y\n" || outcome.Stderr != "" || outcome.Status != 0 || outcome.Refusal != nil {
			t.Fatalf("head <(yes) outcome: %+v", outcome)
		}
	}
	if _, err := exec.LookPath("python3"); err == nil {
		outcome := callComposedProgram(t, f, `python3 -c 'import sys; print(open(sys.argv[1]).read().strip())' <(printf 'fifo-in\n')`)
		if outcome.Stdout != "fifo-in\n" || outcome.Stderr != "" || outcome.Status != 0 || outcome.Refusal != nil {
			t.Fatalf("python3 <(...) outcome: %+v", outcome)
		}
		outcome = callComposedProgram(t, f, `python3 -c 'import sys; print(sys.argv[1], sys.argv[2], open(sys.argv[3]).read().strip())' /dev/fd/63 /dev/fd/6 <(printf 'fifo-in\n')`)
		if outcome.Stdout != "/dev/fd/63 /dev/fd/6 fifo-in\n" || outcome.Stderr != "" || outcome.Status != 0 || outcome.Refusal != nil {
			t.Fatalf("python3 literal /dev/fd/63 and /dev/fd/6 next to <(...) outcome: %+v", outcome)
		}
		outcome = callComposedProgram(t, f, `python3 -c 'import sys; k, p = sys.argv[1].split("=", 1); print(k, open(p).read().strip())' --file=<(printf 'embedded-fifo\n')`)
		if outcome.Stdout != "--file embedded-fifo\n" || outcome.Stderr != "" || outcome.Status != 0 || outcome.Refusal != nil {
			t.Fatalf("python3 --file=<(...) outcome: %+v", outcome)
		}
		outcome = callComposedProgram(t, f, `python3 -c 'import sys; a, b, c = sys.argv[1].split(":", 2); print(a, b, open(c).read().strip())' --spec=/dev/fd/63:/dev/fd/6:<(printf 'real-part\n')`)
		if outcome.Stdout != "--spec=/dev/fd/63 /dev/fd/6 real-part\n" || outcome.Stderr != "" || outcome.Status != 0 || outcome.Refusal != nil {
			t.Fatalf("python3 --spec=/dev/fd/63:/dev/fd/6:<(...) outcome: %+v", outcome)
		}
		outcome = callComposedProgram(t, f, `python3 -c 'import sys; f = open(sys.argv[1], "w"); f.write("fifo-out\n"); f.close()' >(tr a-z A-Z > ext_upper.txt); cat ext_upper.txt`)
		if outcome.Stdout != "FIFO-OUT\n" || outcome.Stderr != "" || outcome.Status != 0 || outcome.Refusal != nil {
			t.Fatalf("python3 >(...) outcome: %+v", outcome)
		}
	}
}
