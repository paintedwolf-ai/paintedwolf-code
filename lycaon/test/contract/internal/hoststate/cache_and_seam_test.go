package hoststate_test

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
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

	varsA, err := prompts.Vars(readerA, map[string]any{"verify_command": "./task test:digest", "execution_mode_entered": "investigate"})
	contractcheck.FailErr(t, "Vars A", err)
	varsB, err := prompts.Vars(readerB, map[string]any{"verify_command": "./task lint:fast", "execution_mode_entered": "orchestrate"})
	contractcheck.FailErr(t, "Vars B", err)

	keyA := hoststate.RenderCacheKey("partials/command-surface.md", "rev-1", []string{"u1"}, varsA)
	keyB := hoststate.RenderCacheKey("partials/command-surface.md", "rev-1", []string{"u1"}, varsB)
	if keyA == "" || keyB == "" || keyA == keyB {
		t.Fatalf("RenderCacheKey invalid or collided: keyA=%q keyB=%q", keyA, keyB)
	}

	cache := hoststate.NewRenderCache()
	calls := 0
	out1, hit1, err := cache.RenderOrReuse(keyA, func() (string, error) { calls++; return "rendered-body", nil })
	contractcheck.FailErr(t, "RenderOrReuse miss", err)
	out2, hit2, err := cache.RenderOrReuse(keyA, func() (string, error) { calls++; return "different", nil })
	contractcheck.FailErr(t, "RenderOrReuse hit", err)
	if hit1 || !hit2 || calls != 1 || out1 != "rendered-body" || out2 != "rendered-body" {
		t.Fatalf("unexpected cache behavior: hit1=%t hit2=%t calls=%d out1=%q out2=%q", hit1, hit2, calls, out1, out2)
	}

	auth1 := hoststate.AuthorityDigest([]string{"wait"}, map[string]map[string]any{"wait": {"enum": []string{"process_done"}}}, nil, readerA.Mints(), readerA)
	auth2 := hoststate.AuthorityDigest([]string{"wait"}, map[string]map[string]any{"wait": {"enum": []string{"next_worker_done"}}}, nil, readerA.Mints(), readerA)
	if auth1 == "" || auth2 == "" || auth1 == auth2 {
		t.Fatal("AuthorityDigest invalid or collided across distinct wait schemas")
	}

	authRuns := 0
	contractcheck.FailErr(t, "first CheckAuthority", cache.CheckAuthority(keyA, auth1, func() error { authRuns++; return nil }))
	contractcheck.FailErr(t, "cached CheckAuthority", cache.CheckAuthority(keyA, auth1, func() error { authRuns++; return nil }))
	contractcheck.FailErr(t, "distinct CheckAuthority", cache.CheckAuthority(keyA, auth2, func() error { authRuns++; return nil }))
	if authRuns != 2 {
		t.Fatalf("authRuns = %d, want 2 (cached duplicate skipped, distinct authority re-run)", authRuns)
	}
}

type hiddenStateValue struct {
	Exported string
	secret   string
}

type ptrOnlyStringer struct{ Exported string }

func (p *ptrOnlyStringer) String() string { return "ptr:" + p.Exported }

type plainDataBox struct{ Value string }

func TestRenderCacheSoundEncodingAndAuthorityRetryAndOverlap(t *testing.T) {
	// Methodless plain data values and pointers must hash distinctly without colliding.
	box := plainDataBox{Value: "ok"}
	kVal, err := hoststate.TryRenderCacheKey("tpl", "r1", nil, map[string]any{"box": box})
	contractcheck.FailErr(t, "TryRenderCacheKey box value", err)
	kPtr, err := hoststate.TryRenderCacheKey("tpl", "r1", nil, map[string]any{"box": &box})
	contractcheck.FailErr(t, "TryRenderCacheKey box ptr", err)
	if kVal == "" || kPtr == "" || kVal == kPtr {
		t.Fatalf("methodless value vs pointer collided: kVal=%q kPtr=%q", kVal, kPtr)
	}

	cyclicMap := map[string]any{}
	cyclicMap["self"] = cyclicMap
	cyclicSlice := make([]any, 1)
	cyclicSlice[0] = cyclicSlice
	nowMono := time.Now()
	ptrFmt := ptrOnlyStringer{Exported: "x"}

	for name, badVars := range map[string]map[string]any{
		"cyclic_map":          {"v": cyclicMap},
		"cyclic_slice":        {"v": cyclicSlice},
		"time_with_monotonic": {"v": nowMono},
		"time_stripped_mono":  {"v": nowMono.Round(0)},
		"ptr_only_method_val": {"v": ptrFmt},
		"ptr_only_method_ptr": {"v": &ptrFmt},
		"unexported_struct":   {"v": hiddenStateValue{Exported: "same", secret: "a"}},
		"typed_int_map_key":   {"v": map[int]string{1: "one"}},
		"nan_float":           {"v": math.NaN()},
		"func_value":          {"v": func() string { return "x" }},
	} {
		if _, tryErr := hoststate.TryRenderCacheKey("tpl", "r1", nil, badVars); !errors.Is(tryErr, hoststate.ErrUncacheableRenderInput) {
			t.Fatalf("%s: TryRenderCacheKey err = %v, want ErrUncacheableRenderInput", name, tryErr)
		}
		if gotKey := hoststate.RenderCacheKey("tpl", "r1", nil, badVars); gotKey != "" {
			t.Fatalf("%s: RenderCacheKey = %q, want empty bypass key", name, gotKey)
		}
	}

	// Focused bypass witnesses: time.Now vs Round(0) and val vs &val with pointer-only String()
	// must bypass BOTH RenderOrReuse and CheckAuthority reuse.
	cache := hoststate.NewRenderCache()
	for i, candidate := range []any{nowMono, nowMono.Round(0), ptrFmt, &ptrFmt} {
		rk := hoststate.RenderCacheKey("tpl", "r1", nil, map[string]any{"v": candidate})
		out, hit, rErr := cache.RenderOrReuse(rk, func() (string, error) { return fmt.Sprint(candidate), nil })
		contractcheck.FailErr(t, "RenderOrReuse bypass", rErr)
		if hit || out != fmt.Sprint(candidate) {
			t.Fatalf("candidate %d reused render cache: hit=%t out=%q", i, hit, out)
		}
		authChecks := 0
		contractcheck.FailErr(t, "CheckAuthority bypass 1", cache.CheckAuthority(rk, "auth-valid", func() error { authChecks++; return nil }))
		contractcheck.FailErr(t, "CheckAuthority bypass 2", cache.CheckAuthority(rk, "auth-valid", func() error { authChecks++; return nil }))
		if authChecks != 2 || cache.AuthorityVerified(rk, "auth-valid") {
			t.Fatalf("candidate %d reused authority check: authChecks=%d verified=%t", i, authChecks, cache.AuthorityVerified(rk, "auth-valid"))
		}
	}
	if hits, misses, bypasses := cache.Stats(); hits != 0 || misses != 0 || bypasses != 4 {
		t.Fatalf("unexpected stats after bypasses: hits=%d misses=%d bypasses=%d", hits, misses, bypasses)
	}

	// Uncacheable projected schema in AuthorityDigest must also bypass CheckAuthority reuse.
	reader, err := prompts.DeclaredReader(nil, "implement_investigate")
	contractcheck.FailErr(t, "DeclaredReader", err)
	badAuth := hoststate.AuthorityDigest([]string{"wait"}, map[string]map[string]any{"wait": {"nan": math.NaN()}}, nil, reader.Mints(), reader)
	if badAuth != "" {
		t.Fatalf("expected empty AuthorityDigest on NaN schema, got %q", badAuth)
	}
	badAuthRuns := 0
	contractcheck.FailErr(t, "badAuth 1", cache.CheckAuthority(kVal, badAuth, func() error { badAuthRuns++; return nil }))
	contractcheck.FailErr(t, "badAuth 2", cache.CheckAuthority(kVal, badAuth, func() error { badAuthRuns++; return nil }))
	if badAuthRuns != 2 || cache.AuthorityVerified(kVal, badAuth) {
		t.Fatalf("empty AuthorityDigest did not bypass reuse: runs=%d", badAuthRuns)
	}

	verifyCheckAuthorityRetryAndOverlap(t, cache, kVal, kPtr)
}

func verifyCheckAuthorityRetryAndOverlap(t *testing.T, cache *hoststate.RenderCache, k1, k2 string) {
	t.Helper()
	sentinelErr := errors.New("authority violation")
	attempts := 0
	err := cache.CheckAuthority(k1, "auth-x", func() error { attempts++; return sentinelErr })
	if !errors.Is(err, sentinelErr) || cache.AuthorityVerified(k1, "auth-x") {
		t.Fatalf("failed check recorded as verified: err=%v verified=%t", err, cache.AuthorityVerified(k1, "auth-x"))
	}
	contractcheck.FailErr(t, "retry CheckAuthority", cache.CheckAuthority(k1, "auth-x", func() error { attempts++; return nil }))
	if attempts != 2 || !cache.AuthorityVerified(k1, "auth-x") {
		t.Fatalf("attempts=%d verified=%t, want 2 and true", attempts, cache.AuthorityVerified(k1, "auth-x"))
	}

	synctest.Test(t, func(t *testing.T) {
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
			errs[1] = cache.CheckAuthority(k2, "auth-overlap", func() error { leaderRuns.Add(1); return nil })
		}()
		synctest.Wait()
		close(release)
		wg.Wait()
		if !errors.Is(errs[0], sentinelErr) || !errors.Is(errs[1], sentinelErr) || leaderRuns.Load() != 1 || cache.AuthorityVerified(k2, "auth-overlap") {
			t.Fatalf("overlapping failure mismatch: errs=%v leaderRuns=%d", errs, leaderRuns.Load())
		}
	})
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
				return s.SessionMode == api.SessionModeAgent && s.Posture == api.SessionPostureBuild &&
					s.WorkflowState == hoststate.WorkflowAmbientIdle && s.RootTopology == hoststate.RootsSingleGit &&
					s.Loading == hoststate.LoadingFloorOnly && s.WebSearch && !s.CommandExternalHidden
			}),
		},
		{
			name: "partly_loaded_agent_build",
			state: pickState(func(s hoststate.CoordinatorState) bool {
				return s.SessionMode == api.SessionModeAgent && s.Posture == api.SessionPostureBuild &&
					s.WorkflowState == hoststate.WorkflowAmbientIdle && s.RootTopology == hoststate.RootsSingleGit &&
					s.Loading == hoststate.LoadingPartlyLoaded && s.WebSearch && !s.CommandExternalHidden
			}),
		},
		{
			name: "ask_ambient_idle",
			state: pickState(func(s hoststate.CoordinatorState) bool {
				return s.SessionMode == api.SessionModeAsk && s.Posture == api.SessionPostureBuild &&
					s.WorkflowState == hoststate.WorkflowAmbientIdle && s.RootTopology == hoststate.RootsSingleGit &&
					s.Loading == hoststate.LoadingFloorOnly
			}),
		},
		{
			name: "ask_active_workflow_observe_plan",
			state: pickState(func(s hoststate.CoordinatorState) bool {
				return s.SessionMode == api.SessionModeAsk && s.Posture == api.SessionPostureBuild &&
					s.WorkflowState == hoststate.WorkflowActivePhase && s.RequestedSurfaceID == "observe_plan" &&
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
				ID: "seam-sess", Mode: st.SessionMode, Posture: st.Posture,
				AgentType: prompts.CoordinatorProfileID, WorkspacePath: "/tmp/fixture-primary",
			}
			frame := inject.CoordinatorTurnFrame{
				RunContext: st.RunContext,
				Roster:     &inject.AgentRoster{Effective: slices.Clone(st.SpawnAllowlist)},
				Machine:    inject.Machine{ProfileID: prompts.CoordinatorProfileID, SkillCount: 1},
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
					LoadedTools:        func(string) map[string]bool { return maps.Clone(st.Loaded) },
					ProjectRootCount:   func(context.Context, *api.Session) int { return st.RootCount },
					ContextSurfaceDeps: promptloop.ContextSurfaceDeps{WebSearchEnabled: func() bool { return st.WebSearch }},
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
				SessionID: sess.ID, Session: sess, History: st.History,
				ProfileID: prompts.CoordinatorProfileID, Machine: frame.Machine,
			})
			contractcheck.FailErr(t, "PromptLoop.Run", runErr)
			if len(recorder.requests) != 1 || !reflect.DeepEqual(frozen.Metas, recorder.requests[0].Tools) {
				t.Fatalf("FreezeTurnAuthority != live CompletionRequest.Tools:\nfrozen=%v\nlive=%v", frozen.Offered, recorder.requests)
			}
		})
	}
}
