//go:build unix

package toolhost

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

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
	"github.com/lycaon/lycaon/internal/tools/native/mutation"
)

func newBB04HTTPOwnerFixture(t *testing.T, gate tools.LoopbackConnectGate) *programtest.Fixture {
	t.Helper()
	f := programtest.New(t, programtest.Options{Tools: []string{"http_request"}})
	classes, err := toolcontract.NewCommandEffects(
		toolcontract.CommandReadOnly,
		toolcontract.CommandMutation,
		toolcontract.CommandExternalExec,
		toolcontract.CommandNetworkEgress,
	)
	testutil.FailErr(t, "compile HTTP profile effects", err)
	b := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true, RejectSymlinkEscape: true}, []sandbox.ToolProfile{{
		ID:             "fixture",
		Tools:          map[string]bool{"command": true, "http_request": true},
		CommandEffects: classes,
	}})
	f.Runtime.Boundary = b
	f.Runtime.Executor = toolexecution.NewExecutor(toolprofiles.NewProfilePolicyEngine(b), f.Runtime.Registry, "fixture")
	if gate != nil {
		f.Runtime.Executor.Capabilities.SetLoopbackConnectGate(gate)
	}
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
	testutil.FailErr(t, "compose HTTP owners", err)
	f.Runtime.Owners = owners
	releaseEgress := confine.SetEgressResolver(func(context.Context, confine.EgressCommand, egressproxy.Endpoint, *confine.EgressDetectionCitation) bool {
		return true
	})
	t.Cleanup(func() { _ = releaseEgress(context.Background()) })
	return f
}

// TestBB04RealOwnerUncertainOutcomePreservesEarlierEffectsAndReuses exercises
// real filesystem and HTTP owners together (distinct from Family B's fake
// staged publisher): a committed write followed by a dropped-response POST
// records both the completed file effect and the unknown HTTP effect, disables
// replay, preserves earlier bytes on disk, and allows subsequent session reuse.
func TestBB04RealOwnerUncertainOutcomePreservesEarlierEffectsAndReuses(t *testing.T) {
	f := newBB04HTTPOwnerFixture(t, bb04LoopbackGate{})
	f.Configure = func(tc *tools.ToolContext) {
		tc.Local.LoopbackConnectGranted = true
	}

	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if hj, ok := w.(http.Hijacker); ok {
			if conn, _, err := hj.Hijack(); err == nil {
				_ = conn.Close()
				return
			}
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	src := fmt.Sprintf("printf 'committed-prior\\n' > prior.txt; curl -sS -X POST -d 'op=1' '%s/commit'", srv.URL)
	out := callComposedProgram(t, f, src)
	if out.State != "completed" || out.Status != 52 || out.Refusal != nil {
		t.Fatalf("real-owner uncertain outcome = %+v refusal=%+v", out, out.Refusal)
	}
	if out.CompletedEffects < 1 || out.UncertainEffects != 1 || out.ReplayAvailable {
		t.Fatalf("unexpected effect accounting: completed=%d uncertain=%d replay=%v",
			out.CompletedEffects, out.UncertainEffects, out.ReplayAvailable)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("server hits = %d, want 1", got)
	}
	priorBytes, err := os.ReadFile(filepath.Join(f.Dir, "prior.txt"))
	testutil.FailErr(t, "read prior.txt", err)
	if string(priorBytes) != "committed-prior\n" {
		t.Fatalf("prior.txt = %q, want %q", priorBytes, "committed-prior\n")
	}

	var completedWrites, unknownHTTP int
	testutil.FailErr(t, "audit execution certainties", f.Database.QueryRowContext(t.Context(), `
		SELECT
			count(*) FILTER (WHERE effect_certainty = 'completed'),
			count(*) FILTER (WHERE operation_id = 'http_request' AND effect_certainty = 'unknown')
		FROM invocation_program_executions
		WHERE program_id = ?`, out.ProgramID).Scan(&completedWrites, &unknownHTTP))
	if completedWrites < 1 || unknownHTTP != 1 {
		t.Fatalf("receipt certainties: completed=%d unknownHTTP=%d", completedWrites, unknownHTTP)
	}

	// Prove clean session reuse after an uncertain effect.
	reuse := callComposedProgram(t, f, "printf 'reused-after-uncertain\\n' > after.txt")
	if reuse.State != "completed" || reuse.Status != 0 || reuse.CompletedEffects != 1 || reuse.UncertainEffects != 0 {
		t.Fatalf("post-uncertain reuse outcome = %+v", reuse)
	}
	afterBytes, err := os.ReadFile(filepath.Join(f.Dir, "after.txt"))
	testutil.FailErr(t, "read after.txt", err)
	if string(afterBytes) != "reused-after-uncertain\n" {
		t.Fatalf("after.txt = %q, want %q", afterBytes, "reused-after-uncertain\n")
	}
}

type bb04CheckpointGate struct {
	calls atomic.Int32
	await func(context.Context, tools.LoopbackConnectAsk) (tools.LoopbackConnectResult, error)
}

func (g *bb04CheckpointGate) Await(ctx context.Context, ask tools.LoopbackConnectAsk) (tools.LoopbackConnectResult, error) {
	g.calls.Add(1)
	return g.await(ctx, ask)
}

func (g *bb04CheckpointGate) SessionLoopbackGrant(context.Context, string, string) (bool, []uint16) {
	return false, nil
}

// TestBB04M11L05CheckpointResumeBoundary exercises the owning approval
// checkpoint boundary (LoopbackConnectGate) for both explicit denial (M12) and
// a bounded state-memory failure across checkpoint resume (L05 boundary),
// verifying single settlement with no retry loop, preservation of earlier
// committed effects, and clean reuse with an allowed alternative.
func TestBB04M11L05CheckpointResumeBoundary(t *testing.T) {
	var serverHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serverHits.Add(1)
		_, _ = w.Write([]byte("ok\n"))
	}))
	defer srv.Close()

	t.Run("denied-checkpoint-preserves-prior-and-allows-alternative", func(t *testing.T) {
		gate := &bb04CheckpointGate{
			await: func(_ context.Context, _ tools.LoopbackConnectAsk) (tools.LoopbackConnectResult, error) {
				return tools.LoopbackConnectResult{Denied: true, UserGuidance: "use local fixture instead"}, nil
			},
		}
		f := newBB04HTTPOwnerFixture(t, gate)
		hitsBefore := serverHits.Load()

		raw, err := f.Call(t, t.Context(), fmt.Sprintf("printf 'prior-before-deny\\n' > prior.txt; curl -sS '%s/ok'", srv.URL))
		if err == nil {
			out := decodeProgramOutcome(t, raw)
			if out.State != "rejected" || out.Refusal == nil || out.CompletedEffects != 1 {
				t.Fatalf("denied checkpoint outcome = %+v refusal=%+v", out, out.Refusal)
			}
		}
		if gate.calls.Load() != 1 {
			t.Fatalf("denied checkpoint retried: Await calls = %d, want 1", gate.calls.Load())
		}
		if serverHits.Load() != hitsBefore {
			t.Fatalf("denied checkpoint reached server: hits delta = %d", serverHits.Load()-hitsBefore)
		}
		gotPrior, err := os.ReadFile(filepath.Join(f.Dir, "prior.txt"))
		testutil.FailErr(t, "read prior.txt", err)
		if string(gotPrior) != "prior-before-deny\n" {
			t.Fatalf("prior.txt = %q", gotPrior)
		}

		alt := callComposedProgram(t, f, "printf 'allowed-alternative\\n' > alt.txt")
		if alt.State != "completed" || alt.Status != 0 || alt.CompletedEffects != 1 {
			t.Fatalf("allowed alternative outcome = %+v", alt)
		}
	})

	t.Run("bounded-state-memory-failure-at-checkpoint-resume-no-retry-loop", func(t *testing.T) {
		gate := &bb04CheckpointGate{
			await: func(_ context.Context, _ tools.LoopbackConnectAsk) (tools.LoopbackConnectResult, error) {
				return tools.LoopbackConnectResult{}, &toolcontract.ProgramMemoryLimitError{
					Account:  "state",
					Maximum:  64 << 20,
					Observed: (64 << 20) + 1,
				}
			},
		}
		f := newBB04HTTPOwnerFixture(t, gate)
		hitsBefore := serverHits.Load()

		raw, err := f.Call(t, t.Context(), fmt.Sprintf("printf 'prior-before-cap\\n' > prior.txt; curl -sS '%s/ok'", srv.URL))
		if err == nil {
			out := decodeProgramOutcome(t, raw)
			if out.State == "completed" && out.Status == 0 {
				t.Fatalf("state-cap checkpoint failure unexpectedly succeeded: %+v", out)
			}
		} else if !strings.Contains(err.Error(), "memory exceeded") && !strings.Contains(err.Error(), "COMMAND_LIMIT_EXCEEDED") {
			t.Logf("checkpoint resume returned error: %v", err)
		}
		if gate.calls.Load() != 1 {
			t.Fatalf("L05 checkpoint resume looped: Await calls = %d, want 1", gate.calls.Load())
		}
		if serverHits.Load() != hitsBefore {
			t.Fatalf("state-cap checkpoint failure reached server")
		}
		gotPrior, err := os.ReadFile(filepath.Join(f.Dir, "prior.txt"))
		testutil.FailErr(t, "read prior.txt", err)
		if string(gotPrior) != "prior-before-cap\n" {
			t.Fatalf("prior.txt = %q", gotPrior)
		}

		// Subsequent command in the same session succeeds cleanly.
		reuse := callComposedProgram(t, f, "printf 'reused-after-cap\\n' > reuse.txt")
		if reuse.State != "completed" || reuse.Status != 0 || reuse.CompletedEffects != 1 {
			t.Fatalf("post-checkpoint-failure reuse outcome = %+v", reuse)
		}
	})
}
