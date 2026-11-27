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
	"net/http/httptest"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providers/openaicompat"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type tb09ManualClock struct {
	mu  sync.Mutex
	now time.Time
}

func newTB09ManualClock(start time.Time) *tb09ManualClock {
	return &tb09ManualClock{now: start.UTC()}
}

func (c *tb09ManualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *tb09ManualClock) Advance(d time.Duration) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	return c.now
}

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

// TestWEATB09_A10_OutboundCardHeldPastModelDeadlineManualClock fulfills the A10
// in-process ledger slot: an outbound secret-screen card held past a model
// deadline under a manual clock and deterministic barrier, driving the real
// owning services (`internal/llm.Registry`, `ModelSecretScreen`, `CallBudget`,
// and `RegistrySummarizer`) against recording HTTP fixture endpoints.
//
// Invariants verified:
//  1. Holding the outbound secret card past the model deadline does NOT start or
//     expire `CallBudget` while screening is pending, and when resolved as
//     `SendRedacted` after advancing the manual clock past the deadline, the
//     request dispatches with a fresh budget window and zero raw canary bytes.
//  2. Holding the outbound secret card past the model deadline until it ends
//     `Unanswered` returns `ErrModelRequestSecretUnanswered` (`SecretScreenBlocked`),
//     sends zero requests to either the primary or secondary fallback provider,
//     and never degrades to a truncating fallback carrying raw canary bytes.
func TestWEATB09_A10_OutboundCardHeldPastModelDeadlineManualClock(t *testing.T) {
	ctx := t.Context()
	overlayPath, overlaySHA := tb09OverlayIdentity(t)

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

	// Recording HTTP fixture endpoints (primary + secondary fallback).
	var primaryRequests atomic.Int32
	var primarySawCanary atomic.Bool
	var primarySawRedacted atomic.Bool
	primarySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		primaryRequests.Add(1)
		body, _ := io.ReadAll(r.Body)
		if bytes.Contains(body, []byte(canary)) {
			primarySawCanary.Store(true)
		}
		if bytes.Contains(body, []byte("[REDACTED]")) {
			primarySawRedacted.Store(true)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"redacted-ok"}}]}`))
	}))
	t.Cleanup(primarySrv.Close)

	var secondaryRequests atomic.Int32
	secondarySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondaryRequests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"unexpected-failover"}}]}`))
	}))
	t.Cleanup(secondarySrv.Close)

	// Manual clock setup (D3).
	t0 := time.Date(2026, 11, 27, 21, 0, 0, 0, time.UTC)
	const modelDeadlineWindow = 15 * time.Second
	modelDeadlineAt := t0.Add(modelDeadlineWindow)
	clk := newTB09ManualClock(t0)

	// --- Phase 1: Card held past model deadline under manual clock, then resolved SendRedacted ---
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
	primaryDriver := openaicompat.New("lite", primarySrv.URL, "", []modelinfo.Entry{{ID: "lite-model"}})
	testutil.FailErr(t, "Register primary", reg1.Register(primaryDriver))
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

	// Wait until the outbound secret card is actively held.
	<-gate1.entered
	if primaryRequests.Load() != 0 {
		t.Fatalf("primary endpoint received %d requests while secret card was held", primaryRequests.Load())
	}
	// Advance manual clock past the model deadline while the card is held.
	t1 := clk.Advance(modelDeadlineWindow + 5*time.Second)
	if !t1.After(modelDeadlineAt) {
		t.Fatalf("manual clock t1=%v did not cross model deadline %v", t1, modelDeadlineAt)
	}
	if !budget.started.IsZero() {
		t.Fatalf("CallBudget started while outbound secret card was still held")
	}
	gate1.Release()

	res1 := <-done1
	testutil.FailErr(t, "post-hold SendRedacted completion", res1.err)
	if budgetStartedDuringHold.Load() {
		t.Fatalf("CallBudget started during secret screen hold")
	}
	if screenCtxHadDeadline.Load() {
		t.Fatalf("secret screen ask context unexpectedly carried a model call deadline")
	}
	if budget.started.IsZero() || budget.Expired() {
		t.Fatalf("post-hold CallBudget state invalid: started=%v expired=%v", !budget.started.IsZero(), budget.Expired())
	}
	if primaryRequests.Load() != 1 || primarySawCanary.Load() || !primarySawRedacted.Load() {
		t.Fatalf("primary endpoint observation mismatch: reqs=%d saw_canary=%v saw_redacted=%v digest=sha256:%s",
			primaryRequests.Load(), primarySawCanary.Load(), primarySawRedacted.Load(), canaryDigest)
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
	primaryDriver2 := openaicompat.New("lite", primarySrv.URL, "", []modelinfo.Entry{{ID: "lite-model"}})
	secondaryDriver2 := openaicompat.New("coord", secondarySrv.URL, "", []modelinfo.Entry{{ID: "coord-model"}})
	testutil.FailErr(t, "Register lite2", reg2.Register(primaryDriver2))
	testutil.FailErr(t, "Register coord2", reg2.Register(secondaryDriver2))
	reg2.SetOutboundSecretScreen(screen2)

	policy := newPolicyFixture(t, ModelPolicy{
		Coordinator: ModelRef{ProviderID: "coord", Model: "coord-model"},
		Lite:        ModelRef{ProviderID: "lite", Model: "lite-model"},
	})
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
	t2 := clk.Advance(modelDeadlineWindow + 5*time.Second)
	gate2.Release()

	res2 := <-done2
	if !errors.Is(res2.err, ErrModelRequestSecretUnanswered) || !SecretScreenBlocked(res2.err) {
		t.Fatalf("unanswered held card error = %v, want ErrModelRequestSecretUnanswered (SecretScreenBlocked)", res2.err)
	}
	if res2.out != "" {
		t.Fatalf("unanswered held card returned non-empty fallback summary")
	}
	if primaryRequests.Load() != 1 {
		t.Fatalf("unanswered held card still contacted primary endpoint: reqs=%d want 1", primaryRequests.Load())
	}
	if secondaryRequests.Load() != 0 {
		t.Fatalf("unanswered held card failed over to secondary endpoint: reqs=%d want 0", secondaryRequests.Load())
	}
	if strings.Contains(res2.err.Error(), canary) {
		t.Fatalf("unanswered error leaked raw canary digest=sha256:%s", canaryDigest)
	}

	receipt := map[string]any{
		"schema":                 "wea-tb09-a10-inproc-v1",
		"slot_id":                "A10-inproc-card-held-past-model-deadline",
		"case_family":            "A10",
		"lane":                   "in-process",
		"owning_service":         "internal/llm.Registry+ModelSecretScreen+CallBudget+RegistrySummarizer",
		"product_git_head":       tb09GitRev("HEAD"),
		"product_git_tree":       tb09GitRev("HEAD^{tree}"),
		"fixture_overlay_path":   overlayPath,
		"fixture_overlay_sha256": overlaySHA,
		"canary_sha256_prefix":   canaryDigest,
		"manual_clock": map[string]any{
			"t0":                          t0.Format(time.RFC3339),
			"model_deadline":              modelDeadlineAt.Format(time.RFC3339),
			"t1_past_deadline_redacted":   t1.Format(time.RFC3339),
			"t2_past_deadline_unanswered": t2.Format(time.RFC3339),
			"model_deadline_window_ms":    modelDeadlineWindow.Milliseconds(),
		},
		"observations": map[string]any{
			"alert_surface":                     string(alertSurface),
			"alert_destination_label":           alertDestLabel,
			"budget_started_during_screen_hold": budgetStartedDuringHold.Load(),
			"screen_ctx_had_deadline":           screenCtxHadDeadline.Load(),
			"post_hold_redacted_send_ok":        res1.err == nil,
			"post_hold_budget_expired":          budget.Expired(),
			"primary_endpoint_requests":         primaryRequests.Load(),
			"primary_endpoint_saw_canary":       primarySawCanary.Load(),
			"primary_endpoint_saw_redacted":     primarySawRedacted.Load(),
			"secondary_failover_requests":       secondaryRequests.Load(),
			"unanswered_err_blocked_failover":   SecretScreenBlocked(res2.err),
			"unanswered_fallback_summary_empty": res2.out == "",
		},
	}
	encoded, err := json.Marshal(receipt)
	testutil.FailErr(t, "marshal A10 in-process receipt", err)
	if bytes.Contains(encoded, []byte(canary)) {
		t.Fatalf("receipt leaked raw canary digest=sha256:%s", canaryDigest)
	}
	t.Logf("WEA_TB09_A10_INPROC_RECEIPT:%s", encoded)
}
