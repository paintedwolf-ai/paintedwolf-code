package hoststate_test

import (
	"context"
	"errors"
	"maps"
	"math"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/toolpolicy"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/hoststate"
)

func TestRenderCacheKeyDistinguishesNonReaderVars(t *testing.T) {
	readerA, err := prompts.DeclaredReader(nil, "implement_investigate")
	contractcheck.FailErr(t, "DeclaredReader A", err)
	readerB, err := prompts.DeclaredReader(nil, "implement_investigate")
	contractcheck.FailErr(t, "DeclaredReader B", err)
	if readerA.Fingerprint() != readerB.Fingerprint() {
		t.Fatalf("expected matching Reader.Fingerprint(), got %s vs %s", readerA.Fingerprint(), readerB.Fingerprint())
	}

	varsA, err := prompts.Vars(readerA, map[string]any{
		"session_scratch_dir":     "/tmp/scratch-a",
		"reviews_confined_writes": false,
	})
	contractcheck.FailErr(t, "Vars A", err)
	varsB, err := prompts.Vars(readerB, map[string]any{
		"session_scratch_dir":     "/tmp/scratch-b",
		"reviews_confined_writes": true,
	})
	contractcheck.FailErr(t, "Vars B", err)

	keyA := hoststate.RenderCacheKey("partials/command-surface.md", "rev-1", []string{"u1"}, varsA)
	keyB := hoststate.RenderCacheKey("partials/command-surface.md", "rev-1", []string{"u1"}, varsB)
	if keyA == "" || keyB == "" || keyA == keyB {
		t.Fatalf("RenderCacheKey invalid or collided: keyA=%q keyB=%q", keyA, keyB)
	}

	cache := hoststate.NewRenderCache()
	calls := 0
	out1, hit1, err := cache.RenderOrReuse(keyA, func() (string, error) {
		calls++
		return "rendered-body", nil
	})
	contractcheck.FailErr(t, "RenderOrReuse miss", err)
	out2, hit2, err := cache.RenderOrReuse(keyA, func() (string, error) {
		calls++
		return "different", nil
	})
	contractcheck.FailErr(t, "RenderOrReuse hit", err)
	if hit1 || !hit2 || calls != 1 || out1 != "rendered-body" || out2 != "rendered-body" {
		t.Fatalf("unexpected cache behavior: hit1=%t hit2=%t calls=%d out1=%q out2=%q", hit1, hit2, calls, out1, out2)
	}

	auth1 := hoststate.AuthorityDigest(
		[]string{"wait"},
		map[string]map[string]any{"wait": {"enum": []string{"process_done"}}},
		nil,
		readerA.Mints(),
		readerA,
	)
	auth2 := hoststate.AuthorityDigest(
		[]string{"wait"},
		map[string]map[string]any{"wait": {"enum": []string{"next_worker_done"}}},
		nil,
		readerA.Mints(),
		readerA,
	)
	if auth1 == auth2 {
		t.Fatal("AuthorityDigest collided across distinct wait schemas")
	}

	authRuns := 0
	contractcheck.FailErr(t, "first CheckAuthority", cache.CheckAuthority(keyA, auth1, func() error {
		authRuns++
		return nil
	}))
	contractcheck.FailErr(t, "cached CheckAuthority", cache.CheckAuthority(keyA, auth1, func() error {
		authRuns++
		return nil
	}))
	contractcheck.FailErr(t, "distinct authority CheckAuthority", cache.CheckAuthority(keyA, auth2, func() error {
		authRuns++
		return nil
	}))
	if authRuns != 2 {
		t.Fatalf("authRuns = %d, want 2 (cached duplicate skipped, distinct authority re-run)", authRuns)
	}
}

type hiddenStateValue struct {
	Exported string
	secret   string
}

func (h hiddenStateValue) Label() string { return h.Exported + ":" + h.secret }

func TestRenderCacheSoundEncodingAndAuthorityRetryAndOverlap(t *testing.T) {
	t1 := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 11, 21, 12, 0, 0, 1, time.UTC)
	t3 := t1.In(time.FixedZone("PST", -8*3600))
	k1, err := hoststate.TryRenderCacheKey("tpl", "r1", nil, map[string]any{"ts": t1})
	contractcheck.FailErr(t, "TryRenderCacheKey t1", err)
	k2, err := hoststate.TryRenderCacheKey("tpl", "r1", nil, map[string]any{"ts": t2})
	contractcheck.FailErr(t, "TryRenderCacheKey t2", err)
	k3, err := hoststate.TryRenderCacheKey("tpl", "r1", nil, map[string]any{"ts": t3})
	contractcheck.FailErr(t, "TryRenderCacheKey t3", err)
	if k1 == k2 || k1 == k3 || k2 == k3 {
		t.Fatalf("time.Time keys collided: k1=%s k2=%s k3=%s", k1, k2, k3)
	}

	cache := hoststate.NewRenderCache()
	for name, badVars := range map[string]map[string]any{
		"unexported_struct": {"v": hiddenStateValue{Exported: "same", secret: "a"}},
		"typed_int_map_key": {"v": map[int]string{1: "one"}},
		"nan_float":         {"v": math.NaN()},
		"func_value":        {"v": func() string { return "x" }},
	} {
		if _, tryErr := hoststate.TryRenderCacheKey("tpl", "r1", nil, badVars); !errors.Is(tryErr, hoststate.ErrUncacheableRenderInput) {
			t.Fatalf("%s: TryRenderCacheKey err = %v, want ErrUncacheableRenderInput", name, tryErr)
		}
		if gotKey := hoststate.RenderCacheKey("tpl", "r1", nil, badVars); gotKey != "" {
			t.Fatalf("%s: RenderCacheKey = %q, want empty bypass key", name, gotKey)
		}
	}

	vA := hiddenStateValue{Exported: "same", secret: "alpha"}
	vB := hiddenStateValue{Exported: "same", secret: "beta"}
	outA, hitA, err := cache.RenderOrReuse(hoststate.RenderCacheKey("tpl", "r1", nil, map[string]any{"v": vA}), func() (string, error) {
		return vA.Label(), nil
	})
	contractcheck.FailErr(t, "RenderOrReuse unexported A", err)
	outB, hitB, err := cache.RenderOrReuse(hoststate.RenderCacheKey("tpl", "r1", nil, map[string]any{"v": vB}), func() (string, error) {
		return vB.Label(), nil
	})
	contractcheck.FailErr(t, "RenderOrReuse unexported B", err)
	if hitA || hitB || outA == outB {
		t.Fatalf("uncacheable values reused cache: hitA=%t hitB=%t outA=%q outB=%q", hitA, hitB, outA, outB)
	}

	sentinelErr := errors.New("authority violation")
	attempts := 0
	err = cache.CheckAuthority(k1, "auth-x", func() error {
		attempts++
		return sentinelErr
	})
	if !errors.Is(err, sentinelErr) || cache.AuthorityVerified(k1, "auth-x") {
		t.Fatalf("failed check recorded as verified: err=%v verified=%t", err, cache.AuthorityVerified(k1, "auth-x"))
	}
	contractcheck.FailErr(t, "retry CheckAuthority", cache.CheckAuthority(k1, "auth-x", func() error {
		attempts++
		return nil
	}))
	if attempts != 2 || !cache.AuthorityVerified(k1, "auth-x") {
		t.Fatalf("attempts=%d verified=%t, want 2 and true", attempts, cache.AuthorityVerified(k1, "auth-x"))
	}

	started := make(chan struct{})
	release := make(chan struct{})
	var leaderRuns atomic.Int32
	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		errs[0] = cache.CheckAuthority(k2, "auth-overlap", func() error {
			leaderRuns.Add(1)
			close(started)
			<-release
			return sentinelErr
		})
	}()
	<-started
	go func() {
		defer wg.Done()
		errs[1] = cache.CheckAuthority(k2, "auth-overlap", func() error {
			leaderRuns.Add(1)
			return nil
		})
	}()
	time.Sleep(10 * time.Millisecond)
	close(release)
	wg.Wait()
	if !errors.Is(errs[0], sentinelErr) || !errors.Is(errs[1], sentinelErr) || leaderRuns.Load() != 1 {
		t.Fatalf("overlapping failure mismatch: errs=%v leaderRuns=%d", errs, leaderRuns.Load())
	}
	if cache.AuthorityVerified(k2, "auth-overlap") {
		t.Fatal("failed overlapping check marked pair verified")
	}
}

type recordingLLMClient struct {
	mu       sync.Mutex
	requests []modelcall.CompletionRequest
}

func (c *recordingLLMClient) Complete(_ context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	c.mu.Lock()
	c.requests = append(c.requests, req)
	c.mu.Unlock()
	return &modelcall.Completion{Content: "Turn settled."}, nil
}

func (c *recordingLLMClient) Stream(_ context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	c.mu.Lock()
	c.requests = append(c.requests, req)
	c.mu.Unlock()
	ch := make(chan modelcall.StreamChunk, 1)
	ch <- modelcall.StreamChunk{Content: "Turn settled.", Done: true}
	close(ch)
	return ch, nil
}

type seamEvidenceLedger struct{}

func (seamEvidenceLedger) LoadLedger(context.Context, string) (evidence.Ledger, error) {
	return evidence.Ledger{}, nil
}

func (seamEvidenceLedger) WorkerLegs(context.Context, string, time.Time) ([]guidance.EvidenceLeg, error) {
	return nil, nil
}

func TestFreezeTurnAuthorityMatchesPromptLoopRequestSeam(t *testing.T) {
	coordStates, err := hoststate.CoordinatorStates(t.Context())
	contractcheck.FailErr(t, "CoordinatorStates", err)

	pickState := func(pred func(hoststate.CoordinatorState) bool) hoststate.CoordinatorState {
		t.Helper()
		for _, st := range coordStates {
			if !st.Excluded && st.PackOverlayID == "stock" && pred(st) {
				return st
			}
		}
		t.Fatal("no matching CoordinatorState found")
		return hoststate.CoordinatorState{}
	}

	cases := []struct {
		name  string
		state hoststate.CoordinatorState
	}{
		{
			name: "floor_only_agent_build",
			state: pickState(func(s hoststate.CoordinatorState) bool {
				return s.SessionMode == api.SessionModeAgent &&
					s.Posture == api.SessionPostureBuild &&
					s.WorkflowState == hoststate.WorkflowAmbientIdle &&
					s.RootTopology == hoststate.RootsSingleGit &&
					s.Loading == hoststate.LoadingFloorOnly &&
					s.WebSearch && !s.CommandExternalHidden
			}),
		},
		{
			name: "partly_loaded_agent_build",
			state: pickState(func(s hoststate.CoordinatorState) bool {
				return s.SessionMode == api.SessionModeAgent &&
					s.Posture == api.SessionPostureBuild &&
					s.WorkflowState == hoststate.WorkflowAmbientIdle &&
					s.RootTopology == hoststate.RootsSingleGit &&
					s.Loading == hoststate.LoadingPartlyLoaded &&
					s.WebSearch && !s.CommandExternalHidden
			}),
		},
		{
			name: "ask_ambient_idle",
			state: pickState(func(s hoststate.CoordinatorState) bool {
				return s.SessionMode == api.SessionModeAsk &&
					s.Posture == api.SessionPostureBuild &&
					s.WorkflowState == hoststate.WorkflowAmbientIdle &&
					s.RootTopology == hoststate.RootsSingleGit &&
					s.Loading == hoststate.LoadingFloorOnly
			}),
		},
		{
			name: "ask_active_workflow_observe_plan",
			state: pickState(func(s hoststate.CoordinatorState) bool {
				return s.SessionMode == api.SessionModeAsk &&
					s.Posture == api.SessionPostureBuild &&
					s.WorkflowState == hoststate.WorkflowActivePhase &&
					s.RequestedSurfaceID == "observe_plan" &&
					s.RootTopology == hoststate.RootsSingleGit
			}),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := tc.state
			frozen, freezeErr := hoststate.FreezeTurnAuthority(t.Context(), st)
			contractcheck.FailErr(t, "FreezeTurnAuthority", freezeErr)

			reg, exec, _, bundleErr := hoststate.RegistryAndExecutorForOverlay(
				st.Overlay, st.Posture, false, st.WebSearch, st.CommandExternalHidden, st.MCPState,
			)
			contractcheck.FailErr(t, "RegistryAndExecutorForOverlay", bundleErr)

			recorder := &recordingLLMClient{}
			sess := &api.Session{
				ID:            "seam-sess",
				Mode:          st.SessionMode,
				Posture:       st.Posture,
				AgentType:     prompts.CoordinatorProfileID,
				WorkspacePath: "/tmp/fixture-primary",
			}
			frame := inject.CoordinatorTurnFrame{
				RunContext: st.RunContext,
				Roster:     &inject.AgentRoster{Effective: slices.Clone(st.SpawnAllowlist)},
				Machine:    inject.Machine{ProfileID: prompts.CoordinatorProfileID},
			}
			policy := toolpolicy.NewEngine(toolpolicy.EngineDeps{
				ToolLister:       exec.Metadata,
				ProjectRootCount: func(context.Context, *api.Session) int { return st.RootCount },
			})
			loop := promptloop.NewPromptLoop(promptloop.PromptLoopDeps{
				Model: promptloop.ModelDeps{LLM: recorder},
				Context: promptloop.ContextDeps{
					Tools:  reg,
					Policy: policy,
					ResolveEffectiveMode: func(_ context.Context, id string) (store.RootAuthority, error) {
						return store.RootAuthority{RootSessionID: id, Mode: st.SessionMode, Started: true}, nil
					},
					Limits: func(context.Context, *api.Session) settings.SessionLimits {
						return settings.DefaultSessionLimits()
					},
					BuildMessages: func(_ context.Context, _ *api.Session, history []api.Message, out *inject.CoordinatorTurnFrame) ([]api.Message, error) {
						if out != nil {
							*out = frame
						}
						return history, nil
					},
					ImplementSessionState: func(context.Context, *api.Session) surface.ImplementSessionState {
						return st.ImplementState
					},
					LoadedTools:      func(string) map[string]bool { return maps.Clone(st.Loaded) },
					ProjectRootCount: func(context.Context, *api.Session) int { return st.RootCount },
					ContextSurfaceDeps: promptloop.ContextSurfaceDeps{
						WebSearchEnabled: func() bool { return st.WebSearch },
					},
				},
				Projection: promptloop.ProjectionDeps{
					AppendMessages:              func(context.Context, string, ...api.Message) error { return nil },
					UpdateMessage:               func(context.Context, string, string, api.Message) error { return nil },
					AppendDraftVersion:          func(context.Context, string, string, string, string) (int, error) { return 1, nil },
					CountDraftVersions:          func(context.Context, string, string) (int, error) { return 0, nil },
					SettledModelOutputIteration: func(context.Context, string) (int, error) { return 0, nil },
				},
				Closeout: promptloop.CloseoutDeps{
					EvidenceLedger: seamEvidenceLedger{},
					EvaluateCloseoutBlock: func(context.Context, *api.Session, *oar.GuardContext) (*oar.Decision, error) {
						return nil, nil
					},
				},
			})

			prepReq, prepErr := loop.ProjectPreparation(t.Context(), sess, st.History, st.History, frame)
			contractcheck.FailErr(t, "ProjectPreparation", prepErr)
			if !reflect.DeepEqual(frozen.Metas, prepReq.Tools) {
				t.Fatalf("FreezeTurnAuthority != ProjectPreparation.Tools:\nfrozen=%v\nprep=%v", frozen.Offered, prepReq.Tools)
			}

			_, runErr := loop.Run(t.Context(), promptloop.PromptRunInput{
				SessionID: sess.ID,
				Session:   sess,
				History:   st.History,
				ProfileID: prompts.CoordinatorProfileID,
				Machine:   frame.Machine,
			})
			contractcheck.FailErr(t, "PromptLoop.Run", runErr)
			if len(recorder.requests) != 1 {
				t.Fatalf("captured %d LLM requests, want 1", len(recorder.requests))
			}
			liveTools := recorder.requests[0].Tools
			if !reflect.DeepEqual(frozen.Metas, liveTools) {
				t.Fatalf("FreezeTurnAuthority != live CompletionRequest.Tools:\nfrozen=%v\nlive=%v", frozen.Offered, liveTools)
			}
		})
	}
}
