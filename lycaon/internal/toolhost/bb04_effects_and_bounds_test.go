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
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/native/mutation"
)

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

	// 3. Server records receipt before connection drops -> typed uncertainty,
	// no replay availability, and zero implicit resend.
	commitOut := callComposedProgram(t, f, fmt.Sprintf("curl -sS -X POST -d 'op=1' '%s/commit'", srv.URL))
	if commitOut.State != "completed" || commitOut.Status != 52 || commitOut.Refusal != nil {
		t.Fatalf("dropped commit response outcome = %+v refusal=%+v, want completed status=52", commitOut, commitOut.Refusal)
	}
	if commitOut.UncertainEffects != 1 || commitOut.ReplayAvailable || !strings.Contains(commitOut.Stderr, "curl: (52)") {
		t.Fatalf("dropped commit uncertainty/recovery facts = %+v (want UncertainEffects=1 ReplayAvailable=false curl:(52))", commitOut)
	}
	var certainty string
	testutil.FailErr(t, "read http_request certainty", f.Database.QueryRowContext(t.Context(),
		`SELECT effect_certainty FROM invocation_program_executions WHERE program_id=? AND operation_id='http_request'`,
		commitOut.ProgramID).Scan(&certainty))
	if certainty != "unknown" {
		t.Fatalf("dropped commit child effect_certainty = %q, want %q", certainty, "unknown")
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
		if limit == nil || limit.Resource != "screen_work_units" || limit.Maximum != 1 || limit.Observed <= 1 {
			t.Fatalf("L04b work budget error was not classified as structured ScreeningLimit: err=%v limit=%+v", screenErr, limit)
		}
		// Verify no duplicated raw text in the classified error string.
		msg := limit.Error()
		if strings.Count(msg, "screen_work_units: requested ") != 1 {
			t.Fatalf("L04b finding: duplicated work-budget error text: %q", msg)
		}
	})

	// Allocator-bound evidence only; checkpoint-resume boundary behavior is
	// exercised separately in TestBB04M11L05CheckpointResumeBoundary.
	t.Run("L05-allocator-bound/program-state-memory-cap-typed-refusal", func(t *testing.T) {
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
