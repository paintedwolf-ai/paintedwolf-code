//go:build unix

package toolhost

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/egressproxy"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/gitengine"
	"github.com/lycaon/lycaon/internal/httpaction"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/native/mutation"
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

// TestBB04M05MultiRootAndGitIsolation exercises M05: identical relative paths
// across sibling roots, detached root refusal, and ordinary Git operations
// leaving the untouched sibling root intact.
func TestBB04M05MultiRootAndGitIsolation(t *testing.T) {
	f := newPathsFixture(t)
	apiShared := filepath.Join(f.api.Path, "shared.txt")
	webShared := filepath.Join(f.web.Path, "shared.txt")
	testutil.FailErr(t, "seed api shared.txt", os.WriteFile(apiShared, []byte("api-baseline\n"), 0o600))
	testutil.FailErr(t, "seed web shared.txt", os.WriteFile(webShared, []byte("web-baseline\n"), 0o600))

	// Mutating relative shared.txt updates only the active root (api).
	raw, err := f.Call(t, t.Context(), `printf 'api-updated\n' > shared.txt`)
	testutil.FailErr(t, "update active root shared.txt", err)
	out := decodeProgramOutcome(t, raw)
	if out.State != "completed" || out.Status != 0 || out.Refusal != nil {
		t.Fatalf("active root update = %+v", out)
	}
	gotAPI, err := os.ReadFile(apiShared)
	testutil.FailErr(t, "read api shared.txt", err)
	gotWeb, err := os.ReadFile(webShared)
	testutil.FailErr(t, "read web shared.txt", err)
	if string(gotAPI) != "api-updated\n" || string(gotWeb) != "web-baseline\n" {
		t.Fatalf("sibling isolation violated: api=%q web=%q", gotAPI, gotWeb)
	}

	// Detach web root; writing to it must refuse and preserve its bytes.
	f.history.Record(testdbseed.DefaultProjectID, f.roots, []projectroot.RootRef{f.api})
	f.roots = []projectroot.RootRef{f.api}
	raw, err = f.Call(t, t.Context(), fmt.Sprintf("printf 'forbidden\\n' > '%s'", webShared))
	testutil.FailErr(t, "write detached root", err)
	detachedOut := decodeProgramOutcome(t, raw)
	if detachedOut.Refusal == nil || detachedOut.Refusal.Code != tools.RootNotAttachedCode {
		t.Fatalf("detached root write = %+v refusal=%+v", detachedOut, detachedOut.Refusal)
	}
	gotWeb, err = os.ReadFile(webShared)
	testutil.FailErr(t, "re-read web shared.txt", err)
	if string(gotWeb) != "web-baseline\n" {
		t.Fatalf("detached root mutated: %q", gotWeb)
	}

	// Ordinary Git read/write in a separate composed fixture with sibling root.
	gitBin, err := gitengine.BinaryPath()
	testutil.FailErr(t, "locate pinned git", err)
	gf := newComposedProgramFixture(t)
	setup := exec.Command(gitBin, "init", "-q", "-b", "main", gf.Dir)
	setup.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	if combined, err := setup.CombinedOutput(); err != nil {
		t.Fatalf("git init fixture: %v: %s", err, combined)
	}
	testutil.FailErr(t, "seed tracked.txt", os.WriteFile(filepath.Join(gf.Dir, "tracked.txt"), []byte("hello-git\n"), 0o600))
	gitOut := callComposedProgram(t, gf, "git add tracked.txt && git status --porcelain")
	if gitOut.State != "completed" || gitOut.Status != 0 || !strings.Contains(gitOut.Stdout, "A  tracked.txt") {
		t.Fatalf("git add/status outcome = %+v", gitOut)
	}
}

type bb04LoopbackGate struct{}

func (bb04LoopbackGate) Await(_ context.Context, ask tools.LoopbackConnectAsk) (tools.LoopbackConnectResult, error) {
	return tools.LoopbackConnectResult{Authorized: true, Ports: append([]uint16(nil), ask.Ports...)}, nil
}

func (bb04LoopbackGate) SessionLoopbackGrant(context.Context, string, string) (bool, []uint16) {
	return true, nil
}

// TestBB04M06LoopbackHTTPFixture exercises M06 using a fixture-chosen ephemeral
// loopback port and an independent server-side request log:
//  1. Successful download writes exact bytes and records 1 request.
//  2. Interrupted response withholds destination sentinel and does not retry.
//  3. Server-recorded receipt before connection drop produces no implicit resend.
func TestBB04M06LoopbackHTTPFixture(t *testing.T) {
	f := programtest.New(t, programtest.Options{Tools: []string{"http_request"}})
	classes, err := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly, toolcontract.CommandMutation, toolcontract.CommandExternalExec, toolcontract.CommandNetworkEgress)
	testutil.FailErr(t, "compile HTTP profile effects", err)
	b := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true, RejectSymlinkEscape: true}, []sandbox.ToolProfile{{
		ID:             "fixture",
		Tools:          map[string]bool{"command": true, "http_request": true},
		CommandEffects: classes,
	}})
	f.Runtime.Boundary = b
	f.Runtime.Executor = toolexecution.NewExecutor(toolprofiles.NewProfilePolicyEngine(b), f.Runtime.Registry, "fixture")
	f.Runtime.Executor.Capabilities.SetLoopbackConnectGate(bb04LoopbackGate{})
	owners, err := (programOwnerTools{
		http:     httpaction.Deps{Boundary: b},
		boundary: b,
		git:      git.NewManager(),
		registry: f.Runtime.Registry,
		operations: mutation.Operations{
			Write:   &native.WriteTool{Boundary: b},
			Delete:  &native.DeleteTool{Boundary: b},
			Move:    &native.MoveTool{Boundary: b},
			Copy:    &native.CopyTool{Boundary: b},
			Mkdir:   &native.MkdirTool{Boundary: b},
			Chmod:   &native.ChmodTool{Boundary: b},
			Chown:   &native.ChownTool{Boundary: b},
			Times:   &native.TimesTool{Boundary: b},
			Extract: &native.ExtractArchiveTool{Boundary: b},
		},
	}).compose(f.Recorder)
	testutil.FailErr(t, "compose HTTP-enabled owners", err)
	f.Runtime.Owners = owners
	f.Configure = func(tc *tools.ToolContext) {
		tc.Local.LoopbackConnectGranted = true
	}
	releaseEgress := confine.SetEgressResolver(func(context.Context, confine.EgressCommand, egressproxy.Endpoint, *confine.EgressDetectionCitation) bool {
		return true
	})
	t.Cleanup(func() { _ = releaseEgress(context.Background()) })

	var (
		logMu      sync.Mutex
		requestLog []string
		commitHits atomic.Int32
	)
	record := func(entry string) {
		logMu.Lock()
		requestLog = append(requestLog, entry)
		logMu.Unlock()
	}
	countFor := func(entry string) int {
		logMu.Lock()
		defer logMu.Unlock()
		n := 0
		for _, got := range requestLog {
			if got == entry {
				n++
			}
		}
		return n
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		record(r.Method + " " + r.URL.Path)
		switch r.URL.Path {
		case "/ok":
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("hello-loopback\n"))
		case "/interrupted":
			w.Header().Set("Content-Length", "4096")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("partial-prefix-bytes"))
			if fl, ok := w.(http.Flusher); ok {
				fl.Flush()
			}
			if hj, ok := w.(http.Hijacker); ok {
				if conn, _, err := hj.Hijack(); err == nil {
					_ = conn.Close()
				}
			}
		case "/commit":
			// Server durably records receipt before client loses the connection.
			commitHits.Add(1)
			if hj, ok := w.(http.Hijacker); ok {
				if conn, _, err := hj.Hijack(); err == nil {
					_ = conn.Close()
					return
				}
			}
			w.WriteHeader(http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	// 1. Successful download.
	okOut := callComposedProgram(t, f, fmt.Sprintf("curl -sS '%s/ok' -o downloaded.txt", srv.URL))
	if okOut.State != "completed" || okOut.Status != 0 || okOut.Refusal != nil {
		t.Fatalf("loopback download outcome = %+v refusal=%+v", okOut, okOut.Refusal)
	}
	gotDownload, err := os.ReadFile(filepath.Join(f.Dir, "downloaded.txt"))
	testutil.FailErr(t, "read downloaded.txt", err)
	if string(gotDownload) != "hello-loopback\n" {
		t.Fatalf("downloaded.txt = %q, want %q", gotDownload, "hello-loopback\n")
	}
	if n := countFor("GET /ok"); n != 1 {
		t.Fatalf("GET /ok count = %d, want 1", n)
	}

	// 2. Interrupted response preserves sentinel and performs no retry.
	sentinel := filepath.Join(f.Dir, "sentinel.txt")
	testutil.FailErr(t, "seed sentinel.txt", os.WriteFile(sentinel, []byte("prior-sentinel\n"), 0o600))
	intOut := callComposedProgram(t, f, fmt.Sprintf("curl -sS '%s/interrupted' -o sentinel.txt", srv.URL))
	if intOut.Status == 0 && intOut.Refusal == nil {
		t.Fatalf("interrupted HTTP response unexpectedly succeeded: %+v", intOut)
	}
	gotSentinel, err := os.ReadFile(sentinel)
	testutil.FailErr(t, "read sentinel.txt", err)
	if string(gotSentinel) != "prior-sentinel\n" {
		t.Fatalf("interrupted download overwrote sentinel: got %q", gotSentinel)
	}
	if n := countFor("GET /interrupted"); n != 1 {
		t.Fatalf("GET /interrupted count = %d, want 1 (no automatic retry)", n)
	}

	// 3. Server records receipt before connection drops -> zero implicit resend.
	commitOut := callComposedProgram(t, f, fmt.Sprintf("curl -sS -X POST -d 'op=1' '%s/commit'", srv.URL))
	if commitOut.Status == 0 && commitOut.Refusal == nil {
		t.Fatalf("dropped commit response unexpectedly succeeded: %+v", commitOut)
	}
	if hits := commitHits.Load(); hits != 1 {
		t.Fatalf("POST /commit server hits = %d, want exactly 1 (no implicit resend)", hits)
	}
}

// TestBB04M11LimitsCleanupAndHistoricalLeads exercises M11 resource bounds and
// post-limit cleanup/reuse, plus historical leads L04a, L04b, and L05.
func TestBB04M11LimitsCleanupAndHistoricalLeads(t *testing.T) {
	t.Run("M11/limits-and-reuse", func(t *testing.T) {
		f := newComposedProgramFixture(t)

		// Beyond loop_iterations limit -> typed COMMAND_LIMIT_EXCEEDED refusal.
		overLoop := callComposedProgram(t, f, "while :; do :; done")
		if overLoop.State != "rejected" || overLoop.Refusal == nil || overLoop.Refusal.Code != "COMMAND_LIMIT_EXCEEDED" {
			t.Fatalf("loop ceiling outcome = %+v refusal=%+v", overLoop, overLoop.Refusal)
		}

		// Beyond program_bytes limit (64 KiB + 1) -> typed COMMAND_LIMIT_EXCEEDED refusal.
		expectBB04Reject(t, f, "#"+strings.Repeat("x", 65537), "COMMAND_LIMIT_EXCEEDED")

		// Subsequent normal command in the same session succeeds cleanly.
		reuse := callComposedProgram(t, f, "printf 'reused-ok\n'")
		if reuse.State != "completed" || reuse.Status != 0 || reuse.Stdout != "reused-ok\n" || reuse.Refusal != nil {
			t.Fatalf("post-limit reuse failed: %+v", reuse)
		}
	})

	t.Run("L04a/nested-substitution-child-identity", func(t *testing.T) {
		f := newComposedProgramFixture(t)

		// Nested command substitutions in a fresh session.
		nested := callComposedProgram(t, f, `echo "$(echo "$(printf 'deep-%s' ok)")"`)
		if nested.State != "completed" || nested.Status != 0 || nested.Stdout != "deep-ok\n" || nested.Refusal != nil {
			t.Fatalf("nested substitution outcome = %+v refusal=%+v", nested, nested.Refusal)
		}

		// Failing nested command substitution must settle with valid child identities.
		failing := callComposedProgram(t, f, `x=$(printf 'a\n' | grep missing_token); printf 'status=%d\n' "$?"`)
		if failing.State != "completed" || failing.Status != 0 || failing.Stdout != "status=1\n" || failing.Refusal != nil {
			t.Fatalf("failing nested substitution outcome = %+v refusal=%+v", failing, failing.Refusal)
		}

		for _, pid := range []string{nested.ProgramID, failing.ProgramID} {
			var total, missingParent, unsettled int
			testutil.FailErr(t, "inspect child identities", f.Database.QueryRowContext(t.Context(), `
				SELECT
					count(*),
					count(*) FILTER (WHERE parent_execution_id = '' OR node_id = ''),
					count(*) FILTER (WHERE state NOT IN ('completed','failed','refused','interrupted'))
				FROM invocation_program_executions
				WHERE program_id = ?`, pid).Scan(&total, &missingParent, &unsettled))
			if total == 0 || missingParent != 0 || unsettled != 0 {
				t.Fatalf("program %s child identity audit: total=%d missingParent=%d unsettled=%d", pid, total, missingParent, unsettled)
			}
		}
	})

	t.Run("L04b/work-budget-screening-classification", func(t *testing.T) {
		// Reproduce the exact detector work-budget overflow class from L04b
		// (work: requested N, maximum M) through captureprojection.Projector
		// and verify ScreeningLimit classifies it cleanly without raw duplication.
		matcher, err := secretmatch.BuildMatcher(secretmatch.Bundled())
		testutil.FailErr(t, "build bundled secret matcher", err)
		projector := captureprojection.New(matcher, nil)
		mem, err := toolcontract.NewProgramMemory(toolcontract.ProgramMemoryLimits{Working: 8 << 20, State: 8 << 20})
		testutil.FailErr(t, "allocate program memory", err)

		var dst bytes.Buffer
		_, screenErr := projector.ScreenStream(
			t.Context(),
			captureprojection.Scope{SessionID: "bb04-l04b"},
			"capture.native",
			strings.NewReader(strings.Repeat("plain-log-line-0123456789\n", 128)),
			&dst,
			captureprojection.StreamOptions{
				Policy:        secretmatch.StreamPolicy{ManagedEchoes: true},
				WorkingMemory: mem,
				Bounds:        secretmatch.StreamBounds{InputBytes: 1 << 20, OutputBytes: 1 << 20, MemoryBytes: 4 << 20, DetectorWork: 1},
			},
		)
		if screenErr == nil {
			t.Fatal("expected detector work budget overflow, got nil")
		}
		limit := captureprojection.ScreeningLimit(screenErr)
		if limit == nil || limit.Resource != "work" || limit.Maximum != 1 || limit.Observed <= 1 {
			t.Fatalf("L04b work budget error was not classified as structured ScreeningLimit: err=%v limit=%+v", screenErr, limit)
		}
		// Verify no duplicated raw text in the classified error string.
		msg := limit.Error()
		if strings.Count(msg, "work: requested ") != 1 {
			t.Fatalf("L04b finding: duplicated work-budget error text: %q", msg)
		}
	})

	t.Run("L05/program-state-memory-cap-typed-refusal", func(t *testing.T) {
		mem, err := toolcontract.NewProgramMemory(toolcontract.ProgramMemoryLimits{Working: 64 << 20, State: 64 << 20})
		testutil.FailErr(t, "allocate 64 MiB program memory", err)
		release, err := mem.ReserveProgramState(t.Context(), 64<<20)
		testutil.FailErr(t, "reserve at exact 64 MiB boundary", err)
		defer release()

		_, overErr := mem.ReserveProgramState(t.Context(), 1)
		var memLimit *toolcontract.ProgramMemoryLimitError
		if overErr == nil || !errors.As(overErr, &memLimit) || memLimit.Account != "state" || memLimit.Maximum != 64<<20 || memLimit.Observed != (64<<20)+1 {
			t.Fatalf("L05 state cap over-reservation: err=%v limit=%+v", overErr, memLimit)
		}
		release()
		// After releasing, subsequent state reservation succeeds cleanly.
		r2, err := mem.ReserveProgramState(t.Context(), 1024)
		testutil.FailErr(t, "re-reserve after release", err)
		r2()
	})
}
