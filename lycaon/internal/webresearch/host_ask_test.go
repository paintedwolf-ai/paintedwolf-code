package webresearch

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/egressgate"
	"github.com/lycaon/lycaon/internal/egressproxy"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestEgressGateRetainsAttribution(t *testing.T) {
	t.Parallel()
	ctx := egressgate.WithAttribution(context.Background(), confine.EgressCommand{SessionID: "s"})
	state := egressgate.From(ctx)
	if state == nil || state.Command.SessionID != "s" || len(state.Command.DeclaredHosts) != 0 {
		t.Fatalf("state=%+v", state)
	}
}

func TestDoProviderHTTPHostAskSurvivesShortTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`ok`))
	}))
	t.Cleanup(srv.Close)
	allowLoopbackFetch(t)
	prevRelax := providerEgressTestRelax
	providerEgressTestRelax = true
	t.Cleanup(func() { providerEgressTestRelax = prevRelax })

	release := make(chan struct{})
	resolverEntered := make(chan struct{}, 1)
	var enteredOnce sync.Once
	confine.SetEgressResolver(func(ctx context.Context, _ confine.EgressCommand, ep egressproxy.Endpoint, _ *confine.EgressDetectionCitation) bool {
		if _, hasDeadline := ctx.Deadline(); hasDeadline {
			t.Error("host ask inherited the provider I/O deadline")
		}
		enteredOnce.Do(func() { resolverEntered <- struct{}{} })
		select {
		case <-release:
			return true
		case <-ctx.Done():
			t.Errorf("host ask canceled before Allow: %v", ctx.Err())
			return false
		}
	})
	t.Cleanup(func() { confine.SetEgressResolver(nil) })
	confine.SetEgressRuleEvaluator(func(context.Context, confine.EgressCommand, string) confine.EgressRuleResult {
		return confine.EgressRuleResult{Effect: confine.EgressRuleAsk, Pattern: "provider.test"}
	})
	t.Cleanup(func() { confine.SetEgressRuleEvaluator(nil) })
	confine.SetEgressPosture(confine.PostureAsk)
	t.Cleanup(func() { confine.SetEgressPosture(confine.PostureObserve) })

	stop, stopCancel := context.WithCancel(context.Background())
	t.Cleanup(stopCancel)
	ctx := hitl.WithStopContext(context.Background(), stop)
	ctx = egressgate.WithAttribution(ctx, confine.EgressCommand{SessionID: "sess-ask-timeout"})

	u, err := url.Parse(srv.URL)
	testutil.FailErr(t, "url.Parse failed", err)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	testutil.FailErr(t, "http.NewRequestWithContext failed", err)

	errCh := make(chan error, 1)
	var body []byte
	var status int
	go func() {
		var e error
		body, status, e = doProviderHTTP(ctx, req, 1, false) // 1s I/O budget — ask must not consume it
		errCh <- e
	}()

	testutil.Receive(t, "host ask resolver entry", resolverEntered)
	close(release)

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("after Allow, dial must succeed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("doProviderHTTP hung after Allow")
	}
	if status != 200 || string(body) != "ok" {
		t.Fatalf("status=%d body=%q host=%s", status, body, u.Hostname())
	}
}

func TestRunProviderTasksSkipsEarlyCancelDuringHostAsk(t *testing.T) {
	prevZero := fanOutZeroHitTimeout
	fanOutZeroHitTimeout = 80 * time.Millisecond
	t.Cleanup(func() { fanOutZeroHitTimeout = prevZero })

	release := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = egressgate.WithAttribution(ctx, confine.EgressCommand{SessionID: "sess-fanout"})

	var canceled atomic.Bool
	tasks := []fanTask{
		{run: func() providerOutcome {
			select {
			case <-release:
				return providerOutcome{hits: []WebHit{{URL: "https://example.com/a"}}}
			case <-ctx.Done():
				canceled.Store(true)
				return providerOutcome{reason: "timeout", detail: "canceled"}
			}
		}},
		{run: func() providerOutcome {
			select {
			case <-release:
				return providerOutcome{hits: []WebHit{{URL: "https://example.com/b"}}}
			case <-ctx.Done():
				canceled.Store(true)
				return providerOutcome{reason: "timeout", detail: "canceled"}
			}
		}},
	}

	done := make(chan []providerOutcome, 1)
	go func() {
		done <- runProviderTasks(ctx, cancel, tasks, 10)
	}()

	if fanOutZeroHitExpired(ctx, time.Hour) {
		t.Fatal("host-ask attribution must disable the zero-hit cancellation cap")
	}
	if canceled.Load() {
		t.Fatal("fan-out canceled providers while host-ask attribution was active")
	}
	close(release)
	select {
	case outs := <-done:
		if len(outs) != 2 {
			t.Fatalf("outcomes = %d", len(outs))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("runProviderTasks hung")
	}
}

func TestEgressGateSkipsWithoutAttribution(t *testing.T) {
	asks := 0
	confine.SetEgressResolver(func(context.Context, confine.EgressCommand, egressproxy.Endpoint, *confine.EgressDetectionCitation) bool {
		asks++
		return false
	})
	defer confine.SetEgressResolver(nil)
	confine.SetEgressPosture(confine.PostureAsk)
	defer confine.SetEgressPosture(confine.PostureObserve)

	if err := egressgate.AwaitHost(context.Background(), "api.example.test"); err != nil {
		t.Fatalf("no attribution must skip ask: %v", err)
	}
	if asks != 0 {
		t.Fatalf("expected 0 asks, got %d", asks)
	}
}

func TestEgressGateRaisesUnderAttribution(t *testing.T) {
	var asked atomic.Int32
	confine.SetEgressResolver(func(_ context.Context, cmd confine.EgressCommand, ep egressproxy.Endpoint, _ *confine.EgressDetectionCitation) bool {
		asked.Add(1)
		if cmd.SessionID != "sess-1" || ep.Host != "api.example.test" {
			t.Fatalf("unexpected ask cmd=%+v host=%s", cmd, ep.Host)
		}
		return true
	})
	defer confine.SetEgressResolver(nil)
	confine.SetEgressPosture(confine.PostureAsk)
	defer confine.SetEgressPosture(confine.PostureObserve)

	ctx := egressgate.WithAttribution(context.Background(), confine.EgressCommand{
		SessionID:  "sess-1",
		ToolCallID: "tc-1",
	})
	if err := egressgate.AwaitHost(ctx, "api.example.test"); err != nil {
		t.Fatalf("approve must allow dial: %v", err)
	}
	if asked.Load() != 1 {
		t.Fatalf("expected 1 ask, got %d", asked.Load())
	}
	// Cached — second dial to same host must not re-prompt.
	if err := egressgate.AwaitHost(ctx, "api.example.test"); err != nil {
		t.Fatalf("cached allow: %v", err)
	}
	if asked.Load() != 1 {
		t.Fatalf("cached host must not re-ask, got %d", asked.Load())
	}
}

func TestEgressGateDenialRecordsAndRejects(t *testing.T) {
	confine.SetEgressResolver(func(context.Context, confine.EgressCommand, egressproxy.Endpoint, *confine.EgressDetectionCitation) bool {
		return false
	})
	defer confine.SetEgressResolver(nil)
	confine.SetEgressPosture(confine.PostureAsk)
	defer confine.SetEgressPosture(confine.PostureObserve)

	ctx := egressgate.WithAttribution(context.Background(), confine.EgressCommand{SessionID: "sess-deny"})
	err := egressgate.AwaitHost(ctx, "blocked.example.test")
	var denied *egressgate.HostDeniedError
	if !errors.As(err, &denied) || denied.Host != "blocked.example.test" {
		t.Fatalf("want HostDeniedError, got %v", err)
	}
	host, ok := egressgate.Denied(ctx)
	if !ok || host != "blocked.example.test" {
		t.Fatalf("denied host = %q %v", host, ok)
	}
	rej := hostDeniedReject(host)
	if rej.Code != "WEB_SEARCH_HOST_DENIED" {
		t.Fatalf("code = %s", rej.Code)
	}
}

func TestMapFetchToolErrHostDenied(t *testing.T) {
	err := mapFetchToolErr(&egressgate.HostDeniedError{Host: "docs.example.test"})
	rej := &toolrejection.ToolReject{}
	ok := errors.As(err, &rej)
	if !ok || rej.Code != "WEB_SEARCH_HOST_DENIED" {
		t.Fatalf("want WEB_SEARCH_HOST_DENIED, got %T %v", err, err)
	}
}
