package llm

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type tb09Barrier struct {
	entered chan struct{}
	release chan struct{}
}

func newTB09Barrier() *tb09Barrier {
	return &tb09Barrier{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (b *tb09Barrier) Wait(ctx context.Context) error {
	close(b.entered)
	select {
	case <-b.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *tb09Barrier) Release() {
	close(b.release)
}

func tb09OverlayIdentity(t *testing.T) (string, string) {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("unable to resolve fixture overlay path")
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read fixture overlay %s: %v", file, err)
	}
	sum := sha256.Sum256(raw)
	return file, hex.EncodeToString(sum[:])
}

func tb09GitRev(ref string) string {
	out, err := exec.Command("git", "rev-parse", ref).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func tb09SyntheticCanary(label string) (string, string) {
	var buf [12]byte
	if _, err := rand.Read(buf[:]); err != nil {
		panic(err)
	}
	token := "WEA_CANARY_TB09_" + label + "_" + hex.EncodeToString(buf[:])
	sum := sha256.Sum256([]byte(token))
	return token, hex.EncodeToString(sum[:8])
}

// tb09InMemoryRoundTripper is an owned in-memory http.RoundTripper (zero sockets /
// external I/O) safe for use inside a Go `testing/synctest` virtual-clock bubble.
type tb09InMemoryRoundTripper struct {
	canary      string
	blockOnCtx  bool
	requests    atomic.Int32
	sawCanary   atomic.Bool
	sawRedacted atomic.Bool
	lastCtx     context.Context //nolint:containedctx // records ctx for budget deadline/cause assertions
	hadDeadline bool
	deadline    time.Time
}

func (rt *tb09InMemoryRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.requests.Add(1)
	rt.lastCtx = req.Context()
	rt.deadline, rt.hadDeadline = req.Context().Deadline()
	if req.Body != nil {
		body, _ := io.ReadAll(req.Body)
		_ = req.Body.Close()
		if rt.canary != "" && bytes.Contains(body, []byte(rt.canary)) {
			rt.sawCanary.Store(true)
		}
		if bytes.Contains(body, []byte("[REDACTED]")) {
			rt.sawRedacted.Store(true)
		}
	}
	if rt.blockOnCtx {
		<-req.Context().Done()
		return nil, req.Context().Err()
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"redacted-ok"}}]}`)),
		Request:    req,
	}, nil
}

// tb09InMemoryProvider wraps an owned in-memory http.Client so all provider I/O
// stays strictly inside the `testing/synctest` bubble.
type tb09InMemoryProvider struct {
	id        string
	model     string
	transport *tb09InMemoryRoundTripper
}

func (p *tb09InMemoryProvider) ID() string { return p.id }
func (p *tb09InMemoryProvider) Models() []modelcall.ModelInfo {
	return []modelcall.ModelInfo{{ID: p.model}}
}
func (p *tb09InMemoryProvider) Profile() providerprofile.Profile {
	return providerprofile.Profile{}
}

func (p *tb09InMemoryProvider) Complete(ctx context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	payload, err := json.Marshal(req.Messages)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://inmemory.invalid/v1/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	client := &http.Client{Transport: p.transport}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.ReadAll(resp.Body)
	return &modelcall.Completion{
		ProviderID: p.id,
		Model:      req.Model,
		Content:    "redacted-ok",
	}, nil
}

func (p *tb09InMemoryProvider) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	comp, err := p.Complete(ctx, req)
	if err != nil {
		return nil, err
	}
	ch := make(chan modelcall.StreamChunk, 1)
	ch <- modelcall.StreamChunk{Content: comp.Content, Done: true}
	close(ch)
	return ch, nil
}

// TestWEATB09_A10_OutboundCardHeldPastModelDeadlineManualClock fulfills the A10
// in-process ledger slot using Go `testing/synctest` so the SAME virtual clock
// drives `time.Sleep`, `CallBudget.deadline()` (`time.Now()`), and
// `context.WithDeadlineCause`, with an owned in-memory `http.RoundTripper`
// (zero external sockets/I/O inside the bubble).
//
// Invariants verified:
//  1. Holding the outbound secret card past the model deadline under synctest's
//     virtual clock does NOT start or expire `CallBudget` while screening is
//     pending; when resolved as `SendRedacted` after advancing past the deadline,
//     `CallBudget` starts at dispatch (`budget_started_at == t1_past_deadline`)
//     with a full fresh deadline window and zero raw canary bytes on the wire.
//  2. Holding the outbound secret card past the model deadline until it ends
//     `Unanswered` returns `ErrModelRequestSecretUnanswered` (`SecretScreenBlocked`),
//     sends zero requests to primary or secondary failover providers, and never
//     degrades to a truncating fallback.
//  3. Control check inside the same synctest bubble proves that once dispatch
//     starts, advancing the same virtual clock past `modelDeadlineWindow`
//     genuinely expires `CallBudget` (`ErrCallBudgetExceeded` / `DeadlineExceeded`).
func TestWEATB09_A10_OutboundCardHeldPastModelDeadlineManualClock(t *testing.T) {
	// Resolve overlay identity, git revisions, matcher, and policy fixture BEFORE
	// entering the synctest bubble (no external I/O or subprocesses inside bubble).
	overlayPath, overlaySHA := tb09OverlayIdentity(t)
	gitHead := tb09GitRev("HEAD")
	gitTree := tb09GitRev("HEAD^{tree}")

	canary, canaryDigest := tb09SyntheticCanary("A10_DEADLINE")
	matcher := modelScreenMatcher(t)
	fp, err := secretmatch.NewFingerprinter(bytes.Repeat([]byte{0x42}, 32))
	testutil.FailErr(t, "NewFingerprinter", err)
	matcher.SetFingerprinter(fp)
	matcher.SetHarvestSource(func(context.Context) []secretmatch.HarvestedValue {
		return []secretmatch.HarvestedValue{{
			Name:        "TB09_DEADLINE_CANARY",
			Container:   ".env",
			Secret:      canary,
			Fingerprint: fp.Fingerprint(canary),
		}}
	})

	policy := newPolicyFixture(t, ModelPolicy{
		Coordinator: ModelRef{ProviderID: "coord", Model: "coord-model"},
		Lite:        ModelRef{ProviderID: "lite", Model: "lite-model"},
	})

	synctest.Test(t, func(t *testing.T) {
		ctx := t.Context()
		const modelDeadlineWindow = 15 * time.Second
		t0 := time.Now().UTC()
		initialModelDeadline := t0.Add(modelDeadlineWindow)

		primaryRT := &tb09InMemoryRoundTripper{canary: canary}
		secondaryRT := &tb09InMemoryRoundTripper{canary: canary}

		// --- Phase 1: Card held past initial model deadline under synctest virtual clock -> SendRedacted ---
		gate1 := newTB09Barrier()
		budget := NewCallBudget(modelDeadlineWindow)
		var budgetStartedDuringHold atomic.Bool
		var screenCtxHadDeadline atomic.Bool
		var alertSurface secretmatch.ScreenSurface
		var alertDestLabel string

		screen1 := NewModelSecretScreen(matcher, func(askCtx context.Context, alert secretmatch.Alert) (secretmatch.Resolution, error) {
			alertSurface = alert.Surface
			alertDestLabel = alert.DestinationLabel
			if !budget.started.IsZero() {
				budgetStartedDuringHold.Store(true)
			}
			if _, ok := askCtx.Deadline(); ok {
				screenCtxHadDeadline.Store(true)
			}
			if err := gate1.Wait(askCtx); err != nil {
				return secretmatch.Resolution{Decision: secretmatch.Unanswered}, nil
			}
			return secretmatch.Resolution{Decision: secretmatch.SendRedacted}, nil
		})

		reg1 := newTestRegistry()
		testutil.FailErr(t, "Register primary", reg1.Register(&tb09InMemoryProvider{
			id: "lite", model: "lite-model", transport: primaryRT,
		}))
		reg1.SetOutboundSecretScreen(screen1)

		provider1, err := reg1.Get("lite")
		testutil.FailErr(t, "Get lite", err)

		req1 := modelcall.CompletionRequest{
			Model: "lite-model",
			Messages: []api.Message{{
				Role:    api.MessageRoleUser,
				Content: "Inspect synthetic credential: " + canary,
			}},
			Debug: modelcall.RequestDebug{SessionID: "sess-tb09-a10", ProjectID: "proj-tb09-a10", CallID: "a10-phase1"},
		}

		type completeRes struct {
			comp *modelcall.Completion
			err  error
		}
		done1 := make(chan completeRes, 1)
		go func() {
			comp, err := provider1.Complete(WithCallBudget(ctx, budget), req1)
			done1 <- completeRes{comp: comp, err: err}
		}()

		<-gate1.entered
		synctest.Wait()
		if primaryRT.requests.Load() != 0 {
			t.Fatalf("primary transport received %d requests while secret card was held", primaryRT.requests.Load())
		}
		// Advance the REAL synctest virtual clock past initialModelDeadline (15s + 5s = 20s).
		time.Sleep(modelDeadlineWindow + 5*time.Second)
		t1 := time.Now().UTC()
		if !t1.After(initialModelDeadline) {
			t.Fatalf("synctest clock t1=%v did not cross initial model deadline %v", t1, initialModelDeadline)
		}
		if !budget.started.IsZero() {
			t.Fatalf("CallBudget started while outbound secret card was still held")
		}

		gate1.Release()
		res1 := <-done1
		tAfterSend := time.Now().UTC()
		postHoldBudgetExpired := budget.Expired()
		testutil.FailErr(t, "post-hold SendRedacted completion", res1.err)

		if budgetStartedDuringHold.Load() {
			t.Fatalf("CallBudget started during secret screen hold")
		}
		if screenCtxHadDeadline.Load() {
			t.Fatalf("secret screen ask context unexpectedly carried a model call deadline")
		}
		if budget.started.IsZero() || !budget.started.Equal(t1) || budget.Expired() {
			t.Fatalf("post-hold CallBudget invalid: started=%v want=%v expired=%v", budget.started, t1, budget.Expired())
		}
		if !primaryRT.hadDeadline || !primaryRT.deadline.Equal(t1.Add(modelDeadlineWindow)) {
			t.Fatalf("dispatch deadline = %v (had=%v), want %v", primaryRT.deadline, primaryRT.hadDeadline, t1.Add(modelDeadlineWindow))
		}
		if primaryRT.requests.Load() != 1 || primaryRT.sawCanary.Load() || !primaryRT.sawRedacted.Load() {
			t.Fatalf("primary transport observation mismatch: reqs=%d saw_canary=%v saw_redacted=%v digest=sha256:%s",
				primaryRT.requests.Load(), primaryRT.sawCanary.Load(), primaryRT.sawRedacted.Load(), canaryDigest)
		}
		if alertSurface != secretmatch.SurfaceModel || alertDestLabel == "" {
			t.Fatalf("unexpected alert metadata: surface=%q dest=%q", alertSurface, alertDestLabel)
		}

		// --- Phase 2: Card held past model deadline -> ends Unanswered -> blocks failover & truncation ---
		gate2 := newTB09Barrier()
		screen2 := NewModelSecretScreen(matcher, func(askCtx context.Context, _ secretmatch.Alert) (secretmatch.Resolution, error) {
			if err := gate2.Wait(askCtx); err != nil {
				return secretmatch.Resolution{Decision: secretmatch.Unanswered}, nil
			}
			return secretmatch.Resolution{Decision: secretmatch.Unanswered}, nil
		})

		reg2 := newTestRegistry()
		testutil.FailErr(t, "Register lite2", reg2.Register(&tb09InMemoryProvider{
			id: "lite", model: "lite-model", transport: primaryRT,
		}))
		testutil.FailErr(t, "Register coord2", reg2.Register(&tb09InMemoryProvider{
			id: "coord", model: "coord-model", transport: secondaryRT,
		}))
		reg2.SetOutboundSecretScreen(screen2)

		summarizer := &RegistrySummarizer{
			Registry:  reg2,
			Policy:    policy,
			Scope:     SettingsScopeGlobal,
			ProjectID: "proj-tb09-a10",
			Class:     UtilityClassQuality,
			Fallback:  compaction.TruncateSummarizer{},
		}

		type sumRes struct {
			out string
			err error
		}
		done2 := make(chan sumRes, 1)
		go func() {
			out, err := summarizer.Summarize(ctx, "Summarize safely.", "Payload with canary: "+canary, 64)
			done2 <- sumRes{out: out, err: err}
		}()

		<-gate2.entered
		synctest.Wait()
		time.Sleep(modelDeadlineWindow + 5*time.Second)
		t2 := time.Now().UTC()
		gate2.Release()

		res2 := <-done2
		if !errors.Is(res2.err, ErrModelRequestSecretUnanswered) || !SecretScreenBlocked(res2.err) {
			t.Fatalf("unanswered held card error = %v, want ErrModelRequestSecretUnanswered (SecretScreenBlocked)", res2.err)
		}
		if res2.out != "" {
			t.Fatalf("unanswered held card returned non-empty fallback summary")
		}
		if primaryRT.requests.Load() != 1 {
			t.Fatalf("unanswered held card still contacted primary transport: reqs=%d want 1", primaryRT.requests.Load())
		}
		if secondaryRT.requests.Load() != 0 {
			t.Fatalf("unanswered held card failed over to secondary transport: reqs=%d want 0", secondaryRT.requests.Load())
		}
		if strings.Contains(res2.err.Error(), canary) {
			t.Fatalf("unanswered error leaked raw canary digest=sha256:%s", canaryDigest)
		}

		// --- Phase 3: Positive clock-coupling proof inside the same synctest bubble ---
		// Proves advancing this same synctest virtual clock genuinely expires CallBudget once dispatched.
		slowRT := &tb09InMemoryRoundTripper{blockOnCtx: true}
		reg3 := newTestRegistry()
		testutil.FailErr(t, "Register slow", reg3.Register(&tb09InMemoryProvider{
			id: "slow", model: "slow-model", transport: slowRT,
		}))
		slowProv, err := reg3.Get("slow")
		testutil.FailErr(t, "Get slow", err)

		expiringBudget := NewCallBudget(modelDeadlineWindow)
		_, slowErr := slowProv.Complete(WithCallBudget(ctx, expiringBudget), modelcall.CompletionRequest{
			Model:    "slow-model",
			Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hello"}},
		})
		tAfterExpiry := time.Now().UTC()
		if !errors.Is(slowErr, context.DeadlineExceeded) || !expiringBudget.Expired() || !errors.Is(context.Cause(slowRT.lastCtx), ErrCallBudgetExceeded) {
			t.Fatalf("synctest budget expiry control failed: err=%v expired=%v cause=%v",
				slowErr, expiringBudget.Expired(), context.Cause(slowRT.lastCtx))
		}

		receipt := map[string]any{
			"schema":                 "wea-tb09-a10-inproc-v1",
			"slot_id":                "A10-inproc-card-held-past-model-deadline",
			"case_family":            "A10",
			"lane":                   "in-process",
			"clock_mechanism":        "testing/synctest",
			"owning_service":         "internal/llm.Registry+ModelSecretScreen+CallBudget+RegistrySummarizer",
			"product_git_head":       gitHead,
			"product_git_tree":       gitTree,
			"fixture_overlay_path":   overlayPath,
			"fixture_overlay_sha256": overlaySHA,
			"canary_sha256_prefix":   canaryDigest,
			"virtual_clock": map[string]any{
				"t0_before_hold":               t0.Format(time.RFC3339),
				"initial_model_deadline":       initialModelDeadline.Format(time.RFC3339),
				"t1_past_deadline_at_release":  t1.Format(time.RFC3339),
				"budget_started_at_dispatch":   budget.started.UTC().Format(time.RFC3339),
				"dispatch_deadline_observed":   primaryRT.deadline.UTC().Format(time.RFC3339),
				"t_after_redacted_send":        tAfterSend.Format(time.RFC3339),
				"t2_past_deadline_unanswered":  t2.Format(time.RFC3339),
				"t3_budget_expiry_control":     tAfterExpiry.Format(time.RFC3339),
				"model_deadline_window_ms":     modelDeadlineWindow.Milliseconds(),
			},
			"observations": map[string]any{
				"alert_surface":                     string(alertSurface),
				"alert_destination_label":           alertDestLabel,
				"budget_started_during_screen_hold": budgetStartedDuringHold.Load(),
				"screen_ctx_had_deadline":           screenCtxHadDeadline.Load(),
				"post_hold_redacted_send_ok":        res1.err == nil,
				"post_hold_budget_expired":          postHoldBudgetExpired,
				"dispatch_deadline_full_window":     primaryRT.deadline.Equal(t1.Add(modelDeadlineWindow)),
				"primary_endpoint_requests":         primaryRT.requests.Load(),
				"primary_endpoint_saw_canary":       primaryRT.sawCanary.Load(),
				"primary_endpoint_saw_redacted":     primaryRT.sawRedacted.Load(),
				"secondary_failover_requests":       secondaryRT.requests.Load(),
				"unanswered_err_blocked_failover":   SecretScreenBlocked(res2.err),
				"unanswered_fallback_summary_empty": res2.out == "",
				"budget_expiry_control_expired":     expiringBudget.Expired(),
			},
		}
		encoded, err := json.Marshal(receipt)
		testutil.FailErr(t, "marshal A10 in-process receipt", err)
		if bytes.Contains(encoded, []byte(canary)) {
			t.Fatalf("receipt leaked raw canary digest=sha256:%s", canaryDigest)
		}
		t.Logf("WEA_TB09_A10_INPROC_RECEIPT:%s", encoded)
	})
}
