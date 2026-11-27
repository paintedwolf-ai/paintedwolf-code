//go:build unix

package toolhost

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/egressproxy"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/httpaction"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/tools/native/mutation"
)

func newBB05HTTPFixture(t *testing.T) *programtest.Fixture {
	t.Helper()
	f := programtest.New(t, programtest.Options{Tools: []string{"http_request"}})
	classes, err := toolcontract.NewCommandEffects(
		toolcontract.CommandReadOnly, toolcontract.CommandMutation,
		toolcontract.CommandExternalExec, toolcontract.CommandNetworkEgress,
	)
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
	return f
}

// TestBB05PartialWriteUnknownHTTP exercises Q15 (partial-write-unknown-http):
//  1. Transport drop after POST receipt: first.txt is preserved, curl settles
//     unknown with zero implicit resend, and dependent touch ./never does not run.
//  2. Supervisor cancellation after POST receipt on the semicolon form
//     (printf 'first\n' > ./first.txt; curl -d payload URL; touch ./never):
//     first.txt is preserved, curl settles interrupted/unknown, touch ./never
//     never starts, and whole-program replay is disallowed.
func TestBB05PartialWriteUnknownHTTP(t *testing.T) {
	var (
		mu       sync.Mutex
		requests []string
	)
	holdReceived := make(chan struct{}, 1)
	holdRelease := make(chan struct{})
	t.Cleanup(func() { close(holdRelease) })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		requests = append(requests, fmt.Sprintf("%s %s %s", r.Method, r.URL.Path, string(body)))
		mu.Unlock()
		switch r.URL.Path {
		case "/commit":
			if hj, ok := w.(http.Hijacker); ok {
				if conn, _, err := hj.Hijack(); err == nil {
					_ = conn.Close()
					return
				}
			}
			w.WriteHeader(http.StatusInternalServerError)
		case "/hold":
			select {
			case holdReceived <- struct{}{}:
			default:
			}
			select {
			case <-holdRelease:
			case <-r.Context().Done():
			case <-time.After(10 * time.Second):
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	countReq := func(want string) int {
		mu.Lock()
		defer mu.Unlock()
		n := 0
		for _, got := range requests {
			if got == want {
				n++
			}
		}
		return n
	}

	t.Run("transport-drop-after-write", func(t *testing.T) {
		f := newBB05HTTPFixture(t)
		cmd := fmt.Sprintf("printf 'first\\n' > ./first.txt && curl -fsS -d payload %s/commit && touch ./never", srv.URL)
		out := callComposedProgram(t, f, cmd)
		gotFirst, readErr := os.ReadFile(filepath.Join(f.Dir, "first.txt"))
		testutil.FailErr(t, "read first.txt", readErr)
		if string(gotFirst) != "first\n" {
			t.Fatalf("first.txt = %q, want %q", gotFirst, "first\n")
		}
		if _, statErr := os.Stat(filepath.Join(f.Dir, "never")); !os.IsNotExist(statErr) {
			t.Fatalf("never must not exist, stat err=%v", statErr)
		}
		if hits := countReq("POST /commit payload"); hits != 1 {
			t.Fatalf("POST /commit payload count = %d, want 1 (no implicit resend)", hits)
		}
		if out.CompletedEffects < 1 || out.UncertainEffects < 1 {
			t.Fatalf("expected completed>=1, uncertain>=1; got %+v", out)
		}
		if out.Status == 0 {
			t.Fatalf("expected non-zero status on dropped POST response, got %+v", out)
		}
	})

	t.Run("supervisor-loss-after-http-semicolon", func(t *testing.T) {
		f := newBB05HTTPFixture(t)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan struct{})
		var (
			raw string
			err error
		)
		cmd := fmt.Sprintf("printf 'first\\n' > ./first.txt; curl -d payload %s/hold; touch ./never", srv.URL)
		go func() {
			defer close(done)
			raw, err = f.Call(t, ctx, cmd)
		}()

		select {
		case <-holdReceived:
			cancel()
		case <-time.After(10 * time.Second):
			cancel()
			t.Fatal("timed out waiting for POST /hold receipt")
		}
		<-done

		gotFirst, readErr := os.ReadFile(filepath.Join(f.Dir, "first.txt"))
		testutil.FailErr(t, "read first.txt", readErr)
		if string(gotFirst) != "first\n" {
			t.Fatalf("first.txt = %q, want %q", gotFirst, "first\n")
		}
		if _, statErr := os.Stat(filepath.Join(f.Dir, "never")); !os.IsNotExist(statErr) {
			t.Fatalf("never must not exist after supervisor loss, stat err=%v", statErr)
		}
		if hits := countReq("POST /hold payload"); hits != 1 {
			t.Fatalf("POST /hold payload count = %d, want 1 (no retry)", hits)
		}
		if err == nil {
			var out command.ProgramOutcome
			testutil.FailErr(t, "decode interrupted outcome", json.Unmarshal([]byte(raw), &out))
			if out.CompletedEffects < 1 || out.UncertainEffects < 1 {
				t.Fatalf("expected completed>=1, uncertain>=1; got %+v", out)
			}
		}
	})
}
