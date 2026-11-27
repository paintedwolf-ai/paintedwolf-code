//go:build unix

package toolhost

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

func expectBB04Reject(t *testing.T, f *programtest.Fixture, source string, wantCodes ...string) {
	t.Helper()
	raw, err := f.Call(t, t.Context(), source)
	if err != nil {
		reject := toolrejection.AsToolReject(err)
		if reject == nil {
			t.Fatalf("%q: expected typed ToolReject, got %T (%v)", source, err, err)
		}
		for _, want := range wantCodes {
			if reject.Code == want {
				return
			}
		}
		t.Fatalf("%q: reject code = %q, want one of %v (err=%v)", source, reject.Code, wantCodes, err)
	}
	outcome := decodeProgramOutcome(t, raw)
	if outcome.State == "rejected" && outcome.Refusal != nil {
		for _, want := range wantCodes {
			if outcome.Refusal.Code == want {
				return
			}
		}
	}
	t.Fatalf("%q: expected rejection %v, got outcome=%+v refusal=%+v", source, wantCodes, outcome, outcome.Refusal)
}

// TestBB04SyntaxGapsToolhostZeroEffects verifies end-to-end through the real
// command entry that the three BB01 syntax gaps refuse the whole unit with
// COMMAND_SYNTAX_UNSUPPORTED, zero executed children, and zero filesystem effects.
func TestBB04SyntaxGapsToolhostZeroEffects(t *testing.T) {
	cases := []struct {
		name   string
		source string
	}{
		{"M01/coproc", "printf mutated > sentinel.txt; coproc sleep 1"},
		{"M01/extglob-negation", "printf mutated > sentinel.txt; echo !(foo)"},
		{"M01/extglob-alternation", "printf mutated > sentinel.txt; echo @(a|b)"},
		{"M04/fd-alloc-var-cat", "printf mutated > sentinel.txt; cat {fd}> out.txt"},
		{"M04/fd-alloc-var-exec", "printf mutated > sentinel.txt; exec {fd}> out.txt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newComposedProgramFixture(t)
			expectBB04Reject(t, f, tc.source, "COMMAND_SYNTAX_UNSUPPORTED")
			var children int
			testutil.FailErr(t, "count child executions", f.Database.QueryRowContext(t.Context(),
				`SELECT count(*) FROM invocation_program_executions`).Scan(&children))
			if children != 0 {
				t.Fatalf("%s: expected zero admitted child executions, got %d", tc.name, children)
			}
			if _, err := os.Stat(filepath.Join(f.Dir, "sentinel.txt")); !os.IsNotExist(err) {
				t.Fatalf("%s: refused unit mutated sentinel.txt: %v", tc.name, err)
			}
		})
	}
}

// TestBB04M01ProfileAndInvalidOptionRefusal exercises M01: valid native calls,
// invalid native options without external fallback, and read-only profile
// enforcement (reads succeed, mutations refused with disk intact).
func TestBB04M01ProfileAndInvalidOptionRefusal(t *testing.T) {
	f := newComposedProgramFixture(t)
	testutil.FailErr(t, "seed sample", os.WriteFile(filepath.Join(f.Dir, "sample.txt"), []byte("alpha\nbeta\n"), 0o600))

	// Valid native calls succeed with zero native gaps.
	okOut := callComposedProgram(t, f, `grep alpha sample.txt | sed 's/alpha/ALPHA/'`)
	if okOut.State != "completed" || okOut.Status != 0 || okOut.Stdout != "ALPHA\n" || okOut.NativeGaps != 0 {
		t.Fatalf("valid native call = %+v", okOut)
	}

	// Invalid options on closed native forms refuse without falling back to external.
	for _, src := range []string{
		"sed --bb04-invalid-option 's/a/b/' sample.txt",
		"lsof -i :0",
	} {
		out := callComposedProgram(t, f, src)
		if out.State != "rejected" || out.Refusal == nil || out.NativeGaps != 0 {
			t.Fatalf("%q: expected typed refusal without fallback, got outcome=%+v refusal=%+v", src, out, out.Refusal)
		}
		var externalStarts int
		testutil.FailErr(t, "check external launches", f.Database.QueryRowContext(t.Context(),
			`SELECT count(*) FROM invocation_program_executions WHERE program_id=? AND native_command='external'`,
			out.ProgramID).Scan(&externalStarts))
		if externalStarts != 0 {
			t.Fatalf("%q: refused native form fell back to %d external launch(es)", src, externalStarts)
		}
	}

	// Read-only profile retains reads and refuses mutations.
	ro := newComposedProgramFixture(t)
	testutil.FailErr(t, "seed ro sample", os.WriteFile(filepath.Join(ro.Dir, "sample.txt"), []byte("readonly-ok\n"), 0o600))
	roClasses, err := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly)
	testutil.FailErr(t, "compile readonly effects", err)
	roBoundary := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true, RejectSymlinkEscape: true},
		[]sandbox.ToolProfile{{ID: "fixture", Tools: map[string]bool{"command": true}, CommandEffects: roClasses}})
	ro.Runtime.Boundary = roBoundary
	ro.Runtime.Executor = toolexecution.NewExecutor(toolprofiles.NewProfilePolicyEngine(roBoundary), ro.Runtime.Registry, "fixture")

	readOut := callComposedProgram(t, ro, "cat sample.txt")
	if readOut.State != "completed" || readOut.Status != 0 || readOut.Stdout != "readonly-ok\n" {
		t.Fatalf("read-only profile blocked read: %+v", readOut)
	}
	mutOut := callComposedProgram(t, ro, "printf mutated > denied.txt")
	if mutOut.State != "rejected" || mutOut.Refusal == nil {
		t.Fatalf("read-only profile permitted mutation: %+v", mutOut)
	}
	if _, err := os.Stat(filepath.Join(ro.Dir, "denied.txt")); !os.IsNotExist(err) {
		t.Fatalf("read-only profile wrote denied.txt: %v", err)
	}
}

// TestBB04M02ExactQuotingAndMalformedRecovery exercises M02: multiline heredoc
// with apostrophe, quoted URL with '&', spaces/newlines/empty operands, binary
// payload preservation, and malformed quote followed by a corrected call.
func TestBB04M02ExactQuotingAndMalformedRecovery(t *testing.T) {
	f := newComposedProgramFixture(t)

	// 1. Multiline heredoc with apostrophes and literal $name/&.
	heredocBody := "it's line one with 'apostrophes'\nline two with $LITERAL & ampersand\n"
	out := callComposedProgram(t, f, "cat <<'EOF' > heredoc.txt\n"+heredocBody+"EOF\n")
	if out.State != "completed" || out.Status != 0 || out.Refusal != nil {
		t.Fatalf("heredoc outcome = %+v", out)
	}
	gotHeredoc, err := os.ReadFile(filepath.Join(f.Dir, "heredoc.txt"))
	testutil.FailErr(t, "read heredoc.txt", err)
	if string(gotHeredoc) != heredocBody {
		t.Fatalf("heredoc bytes = %q, want %q", gotHeredoc, heredocBody)
	}

	// 2. Quoted URL containing '&' must stay literal (not backgrounded).
	urlStr := "https://example.test/api?a=1&b=2&c=3"
	out = callComposedProgram(t, f, fmt.Sprintf("printf '%%s' '%s'", urlStr))
	if out.State != "completed" || out.Status != 0 || out.Stdout != urlStr {
		t.Fatalf("quoted URL outcome = %+v, want stdout %q", out, urlStr)
	}

	// 3. Spaces, newlines, and empty fields in operands.
	out = callComposedProgram(t, f, "printf '[%s]' '' 'two words' 'a\nb'")
	if out.State != "completed" || out.Status != 0 || out.Stdout != "[][two words][a\nb]" {
		t.Fatalf("operand framing outcome = %+v", out)
	}

	// 4. Binary payload with NUL, high bytes, and CRLF preserved across redirect.
	binPayload := []byte{0x00, 0x01, 'A', '\r', '\n', 0x7f, 0xff, 0x00}
	testutil.FailErr(t, "seed bin.in", os.WriteFile(filepath.Join(f.Dir, "bin.in"), binPayload, 0o600))
	out = callComposedProgram(t, f, "cat bin.in > bin.out")
	if out.State != "completed" || out.Status != 0 {
		t.Fatalf("binary redirect outcome = %+v", out)
	}
	gotBin, err := os.ReadFile(filepath.Join(f.Dir, "bin.out"))
	testutil.FailErr(t, "read bin.out", err)
	if !bytes.Equal(gotBin, binPayload) {
		t.Fatalf("binary payload altered: got %x, want %x", gotBin, binPayload)
	}

	// 5. Malformed quote opens zero executable children; corrected call creates
	// its own fresh receipt and performs the effect once.
	var beforeChildren int
	testutil.FailErr(t, "count children before malformed", f.Database.QueryRowContext(t.Context(),
		`SELECT count(*) FROM invocation_program_executions`).Scan(&beforeChildren))
	expectBB04Reject(t, f, "printf changed > recovered.txt; echo 'unterminated", "COMMAND_SYNTAX_INVALID", "COMMAND_SYNTAX_UNSUPPORTED")
	var afterChildren int
	testutil.FailErr(t, "count children after malformed", f.Database.QueryRowContext(t.Context(),
		`SELECT count(*) FROM invocation_program_executions`).Scan(&afterChildren))
	if afterChildren != beforeChildren {
		t.Fatalf("malformed quote opened %d child execution(s)", afterChildren-beforeChildren)
	}
	if _, err := os.Stat(filepath.Join(f.Dir, "recovered.txt")); !os.IsNotExist(err) {
		t.Fatalf("malformed quote created recovered.txt: %v", err)
	}

	fixed := callComposedProgram(t, f, "printf 'corrected\n' > recovered.txt")
	if fixed.State != "completed" || fixed.Status != 0 || fixed.CompletedEffects != 1 || fixed.ProgramID == "" {
		t.Fatalf("corrected call outcome = %+v", fixed)
	}
	gotRecovered, err := os.ReadFile(filepath.Join(f.Dir, "recovered.txt"))
	testutil.FailErr(t, "read recovered.txt", err)
	if string(gotRecovered) != "corrected\n" {
		t.Fatalf("recovered.txt = %q, want %q", gotRecovered, "corrected\n")
	}
}

// TestBB04M03PipelinesAndPublicationIndependence exercises M03: complete
// grep/diff status 1, early consumer close, pipefail contract, producer emits
// prefix then fails (sentinel survives), and typed host stop surviving later clause.
func TestBB04M03PipelinesAndPublicationIndependence(t *testing.T) {
	f := newComposedProgramFixture(t)

	// 1. Complete grep status 1 and diff status 1.
	testutil.FailErr(t, "seed f1", os.WriteFile(filepath.Join(f.Dir, "f1.txt"), []byte("line-a\n"), 0o600))
	testutil.FailErr(t, "seed f2", os.WriteFile(filepath.Join(f.Dir, "f2.txt"), []byte("line-b\n"), 0o600))

	grepOut := callComposedProgram(t, f, "printf 'alpha\nbeta\n' | grep gamma")
	if grepOut.State != "completed" || grepOut.Status != 1 || grepOut.Stdout != "" || grepOut.Refusal != nil {
		t.Fatalf("grep status 1 outcome = %+v", grepOut)
	}

	diffOut := callComposedProgram(t, f, "diff -u f1.txt f2.txt > diff.patch")
	if diffOut.State != "completed" || diffOut.Status != 1 || diffOut.Refusal != nil {
		t.Fatalf("diff status 1 outcome = %+v", diffOut)
	}
	patchBytes, err := os.ReadFile(filepath.Join(f.Dir, "diff.patch"))
	testutil.FailErr(t, "read diff.patch", err)
	if !strings.Contains(string(patchBytes), "-line-a") || !strings.Contains(string(patchBytes), "+line-b") {
		t.Fatalf("complete diff status 1 did not publish diff output: %q", patchBytes)
	}

	// 2. Early consumer close.
	early := callComposedProgram(t, f, "yes | head -n 1")
	if early.State != "completed" || early.Status != 0 || early.Stdout != "y\n" {
		t.Fatalf("early consumer close outcome = %+v", early)
	}

	// 3. Pipefail on (always on in commandprogram) and set +o pipefail refusal.
	pfOn := callComposedProgram(t, f, "set -o pipefail; printf 'a\n' | grep z | cat")
	if pfOn.State != "completed" || pfOn.Status != 1 {
		t.Fatalf("pipefail-on pipeline outcome = %+v", pfOn)
	}
	expectBB04Reject(t, f, "set +o pipefail; printf 'a\n' | grep z | cat", "COMMAND_SYNTAX_UNSUPPORTED")

	// 4. Producer emits prefix then fails -> existing destination sentinel survives.
	sentinel := filepath.Join(f.Dir, "sentinel.txt")
	testutil.FailErr(t, "seed sentinel", os.WriteFile(sentinel, []byte("existing-sentinel\n"), 0o600))
	failProd := callComposedProgram(t, f, `{ printf 'partial-prefix\n'; external -- /bin/sh -c 'exit 7'; } > sentinel.txt`)
	if failProd.State != "completed" || failProd.Status != 7 {
		t.Fatalf("failing producer outcome = %+v", failProd)
	}
	gotSentinel, err := os.ReadFile(sentinel)
	testutil.FailErr(t, "read sentinel", err)
	if string(gotSentinel) != "existing-sentinel\n" {
		t.Fatalf("failed producer overwrote sentinel: got %q, want %q", gotSentinel, "existing-sentinel\n")
	}

	// 5. Typed host stop survives a later || clause.
	stopOut := callComposedProgram(t, f, `{ printf 'before\n'; repo-map --path ../outside; } || printf 'masked\n'`)
	if stopOut.State != "rejected" || stopOut.Refusal == nil || stopOut.Refusal.Code != "SURVEY_PATH_ESCAPE" {
		t.Fatalf("host stop did not survive || clause: %+v", stopOut)
	}
	if strings.Contains(stopOut.Stdout, "masked") {
		t.Fatalf("later || clause executed after host stop: stdout=%q", stopOut.Stdout)
	}
}

// TestBB04M04RedirectsStderrAndLateFailure exercises M04: replace/append,
// stderr routing, explicit target redirection, and late failure preserving
// earlier committed bytes.
func TestBB04M04RedirectsStderrAndLateFailure(t *testing.T) {
	f := newComposedProgramFixture(t)

	out := callComposedProgram(t, f, `
printf 'one\n' > out.txt
printf 'two\n' >> out.txt
printf 'err1\n' 2> err.txt >&2
printf 'err2\n' 2>> err.txt >&2
{ printf 'both-out\n'; printf 'both-err\n' >&2; } &> combined.txt
`)
	if out.State != "completed" || out.Status != 0 || out.Refusal != nil {
		t.Fatalf("redirect forms outcome = %+v", out)
	}
	checkFile := func(name, want string) {
		t.Helper()
		got, err := os.ReadFile(filepath.Join(f.Dir, name))
		testutil.FailErr(t, "read "+name, err)
		if string(got) != want {
			t.Fatalf("%s = %q, want %q", name, got, want)
		}
	}
	checkFile("out.txt", "one\ntwo\n")
	checkFile("err.txt", "err1\nerr2\n")
	checkFile("combined.txt", "both-out\nboth-err\n")

	// Late publication failure preserves earlier committed bytes and target sentinel.
	testutil.FailErr(t, "seed second.txt", os.WriteFile(filepath.Join(f.Dir, "second.txt"), []byte("prior-second\n"), 0o600))
	lateFail := callComposedProgram(t, f, `printf 'committed-first\n' > first.txt; { printf 'incomplete\n'; external -- /bin/sh -c 'exit 5'; } > second.txt`)
	if lateFail.State != "completed" || lateFail.Status != 5 {
		t.Fatalf("late publication failure outcome = %+v", lateFail)
	}
	checkFile("first.txt", "committed-first\n")
	checkFile("second.txt", "prior-second\n")

	// Late host refusal also preserves earlier committed bytes.
	lateRefuse := callComposedProgram(t, f, `printf 'kept-before-refusal\n' > kept.txt; repo-map --path ../outside`)
	if lateRefuse.State != "rejected" || lateRefuse.Refusal == nil || lateRefuse.Refusal.Code != "SURVEY_PATH_ESCAPE" || lateRefuse.CompletedEffects != 1 {
		t.Fatalf("late refusal outcome = %+v", lateRefuse)
	}
	checkFile("kept.txt", "kept-before-refusal\n")
}
