package loopwake

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/promptresult"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/egressproxy"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestParseReadinessConditions(t *testing.T) {
	subscription, err := resolveConditions([]any{
		map[string]any{"kind": "http_ready", "url": "https://example.test/health", "method": "head", "status_min": 204, "status_max": 299},
		map[string]any{"kind": "port_ready", "host": "localhost", "port": 8080},
	})
	if err != nil {
		t.Fatalf("resolve readiness conditions: %v", err)
	}
	if len(subscription.Conditions) != 2 || subscription.Conditions[0].Method != "HEAD" || subscription.Conditions[0].StatusMin != 204 || subscription.Conditions[0].StatusMax != 299 ||
		subscription.Conditions[1].Host != "localhost" || subscription.Conditions[1].Port != 8080 {
		t.Fatalf("conditions = %+v", subscription.Conditions)
	}
	if !hasWaitTrigger(subscription.Triggers, WaitTriggerTimer) || !hasWaitTrigger(subscription.Triggers, WaitTriggerHTTPReady) || !hasWaitTrigger(subscription.Triggers, WaitTriggerPortReady) {
		t.Fatalf("triggers = %v", subscription.Triggers)
	}
}

func TestParseReadinessConditionsRejectsUnsafeShapes(t *testing.T) {
	cases := []any{
		[]any{map[string]any{"kind": "http_ready", "url": "file:///tmp/ready"}},
		[]any{map[string]any{"kind": "http_ready", "url": "https://example.test/health#fragment"}},
		[]any{map[string]any{"kind": "http_ready", "url": "https://example.test", "status_min": 500, "status_max": 200}},
		[]any{map[string]any{"kind": "http_ready", "url": "https://example.test:8080", "port": 443}},
		[]any{map[string]any{"kind": "port_ready", "host": "localhost:8080", "port": 8080}},
		[]any{map[string]any{"kind": "port_ready", "host": "localhost"}},
		[]any{map[string]any{"kind": "next_worker_done", "url": "https://example.test"}},
	}
	for _, raw := range cases {
		if _, err := resolveConditions(raw); err == nil {
			t.Fatalf("unsafe condition accepted: %#v", raw)
		}
	}
}

func TestParseReadinessConditions_HTTPReadyPort(t *testing.T) {
	matching := []any{map[string]any{"kind": "http_ready", "url": "http://127.0.0.1:8765/healthz", "port": 8765}}
	sub, err := resolveConditions(matching)
	if err != nil {
		t.Fatalf("expected valid condition with matching port: %v", err)
	}
	if len(sub.Conditions) != 1 || sub.Conditions[0].URL != "http://127.0.0.1:8765/healthz" {
		t.Fatalf("unexpected conds: %+v", sub.Conditions)
	}

	implicit := []any{map[string]any{"kind": "http_ready", "url": "http://127.0.0.1/healthz", "port": 8765}}
	subImplicit, err := resolveConditions(implicit)
	if err != nil {
		t.Fatalf("expected valid condition with inferred port: %v", err)
	}
	if len(subImplicit.Conditions) != 1 || subImplicit.Conditions[0].URL != "http://127.0.0.1:8765/healthz" {
		t.Fatalf("unexpected conds: %+v", subImplicit.Conditions)
	}
}

func TestValidateProfileConditionsUsesRoleContract(t *testing.T) {
	profiles := map[string]map[string]bool{
		"implement":       {"process_done": true, "http_ready": true, "port_ready": true},
		"worker_readonly": {"http_ready": true, "port_ready": true},
	}
	if err := validateProfileConditions("implement", []awaitstore.Condition{{Kind: "process_done"}}, profiles); err != nil {
		t.Fatalf("implement process_done: %v", err)
	}
	err := validateProfileConditions("worker_readonly", []awaitstore.Condition{{Kind: "process_done"}}, profiles)
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "WAIT_CONDITION_NOT_ALLOWED" {
		t.Fatalf("error = %v, want WAIT_CONDITION_NOT_ALLOWED", err)
	}
	if !reflect.DeepEqual(reject.Data["wait_allowed_conditions"], []string{"http_ready", "port_ready"}) {
		t.Fatalf("active allowed conditions lost: %v", reject.Data)
	}
}

func TestReadinessChecksRequireExactLoopbackAuthority(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	testutil.FailErr(t, "listen for port readiness", err)
	t.Cleanup(func() { _ = listener.Close() })
	port := uint16(listener.Addr().(*net.TCPAddr).Port)
	condition := awaitstore.Condition{Kind: "port_ready", Host: "localhost", Port: port}
	if portConditionSatisfied(t.Context(), awaitstore.Lease{LoopbackPorts: []uint16{port}}, condition) != true {
		t.Fatal("authorized listening port was not ready")
	}
	if portConditionSatisfied(t.Context(), awaitstore.Lease{LoopbackPorts: []uint16{port + 1}}, condition) {
		t.Fatal("port readiness ignored exact authority")
	}
}

func TestHTTPReadinessChecksStatusRange(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	target, err := url.Parse(server.URL)
	testutil.FailErr(t, "parse readiness server", err)
	portNumber, err := strconv.Atoi(target.Port())
	testutil.FailErr(t, "parse readiness port", err)
	lease := awaitstore.Lease{LoopbackPorts: []uint16{uint16(portNumber)}}
	condition := awaitstore.Condition{Kind: "http_ready", URL: server.URL + "/health", Method: "HEAD", StatusMin: 200, StatusMax: 299}
	winner, ready := httpConditionOutcome(t.Context(), lease, condition)
	if !ready || winner.Outcome != "satisfied" {
		t.Fatal("204 response did not satisfy 2xx readiness")
	}
	condition.StatusMax = 203
	if _, ready := httpConditionOutcome(t.Context(), lease, condition); ready {
		t.Fatal("204 response satisfied a range ending at 203")
	}
}

func TestHTTPReadinessDenialIsTerminalStructuredOutcome(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	target, err := url.Parse(server.URL)
	testutil.FailErr(t, "parse denied readiness server", err)
	portNumber, err := strconv.Atoi(target.Port())
	testutil.FailErr(t, "parse denied readiness port", err)
	confine.SetEgressRuleEvaluator(func(context.Context, confine.EgressCommand, string) confine.EgressRuleResult {
		return confine.EgressRuleResult{Effect: confine.EgressRuleAsk, Pattern: "denied.test"}
	})
	t.Cleanup(func() { confine.SetEgressRuleEvaluator(nil) })
	confine.SetEgressResolver(func(context.Context, confine.EgressCommand, egressproxy.Endpoint, *confine.EgressDetectionCitation) bool {
		return false
	})
	t.Cleanup(func() { confine.SetEgressResolver(nil) })
	confine.SetEgressPosture(confine.PostureAsk)
	t.Cleanup(func() { confine.SetEgressPosture(confine.PostureObserve) })

	lease := awaitstore.Lease{
		SessionID: "direct-1", ToolCallID: "wait-1", LoopbackPorts: []uint16{uint16(portNumber)},
	}
	winner, terminal := httpConditionOutcome(t.Context(), lease, awaitstore.Condition{
		Kind: "http_ready", URL: server.URL, Method: "HEAD", StatusMin: 200, StatusMax: 299,
	})
	if !terminal || winner.Outcome != "denied" || winner.Code != "HTTP_REQUEST_HOST_DENIED" {
		t.Fatalf("denied readiness outcome = %+v terminal=%v", winner, terminal)
	}
}

func TestConditionMonitorCannotResolveAfterDeadline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(120 * time.Millisecond)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	target, err := url.Parse(server.URL)
	testutil.FailErr(t, "parse delayed readiness server", err)
	portNumber, err := strconv.Atoi(target.Port())
	testutil.FailErr(t, "parse delayed readiness port", err)

	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertSession(t, database, "session-1", testdbseed.DefaultProjectID)
	store := &awaitstore.Store{DB: database}
	lease, err := store.Arm(t.Context(), awaitstore.Lease{
		SessionID: "session-1", ProjectID: testdbseed.DefaultProjectID, ToolCallID: "call-1",
		ProfileID: "implement", Deadline: time.Now().UTC().Add(40 * time.Millisecond),
		LoopbackPorts: []uint16{uint16(portNumber)}, Conditions: []awaitstore.Condition{{
			Kind: "http_ready", URL: server.URL, Method: "GET", StatusMin: 200, StatusMax: 299,
		}},
	})
	testutil.FailErr(t, "arm delayed readiness wait", err)

	monitorConditions(t.Context(), NewLoopEngine(), store, lease)
	active, ok, err := store.ForSession(t.Context(), lease.SessionID)
	testutil.FailErr(t, "read delayed readiness wait", err)
	if !ok || active.ID != lease.ID {
		t.Fatalf("late readiness settled lease: active=%+v ok=%v", active, ok)
	}
}

func TestRecoverWaitLeasesRebuildsParkedState(t *testing.T) {
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertSession(t, database, "session-1", testdbseed.DefaultProjectID)
	store := &awaitstore.Store{DB: database}
	lease, err := store.Arm(t.Context(), awaitstore.Lease{
		SessionID: "session-1", ProjectID: testdbseed.DefaultProjectID, ToolCallID: "call-1",
		ProfileID: "implement", Deadline: time.Now().UTC().Add(10 * time.Minute), Reason: "recover me",
		Conditions: []awaitstore.Condition{{Kind: "process_done", Handles: []string{"command-1"}}},
	})
	testutil.FailErr(t, "arm recoverable wait", err)

	loop := NewLoopEngine()
	loop.SetDeps(busyWaitLoopDeps())
	if err := RecoverWaitLeases(context.Background(), loop, store); err != nil {
		t.Fatalf("recover wait leases: %v", err)
	}
	if !loop.IsSleeping(lease.SessionID) || !loop.SessionSleepingOnProcess(lease.SessionID, "command-1") {
		t.Fatalf("recovered triggers = %v", loop.WaitSubscriptionForTest(lease.SessionID))
	}
}

func TestRecoverSettledWaitResumesDirectSessionExactlyOnce(t *testing.T) {
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertSession(t, database, "direct-1", testdbseed.DefaultProjectID)
	store := &awaitstore.Store{DB: database}
	lease, err := store.Arm(t.Context(), awaitstore.Lease{
		SessionID: "direct-1", ProjectID: testdbseed.DefaultProjectID, ToolCallID: "call-1",
		ProfileID: "explore_readonly", Deadline: time.Now().UTC().Add(time.Minute),
		Conditions: []awaitstore.Condition{{Kind: "http_ready", URL: "https://example.test/health"}},
	})
	testutil.FailErr(t, "arm direct wait", err)
	winner := awaitstore.Condition{Kind: "http_ready", Outcome: "satisfied", URL: "https://example.test/health"}
	won, err := store.SettleLease(t.Context(), lease.ID, "resolved", winner)
	testutil.FailErr(t, "settle direct wait", err)
	if !won {
		t.Fatal("direct wait settlement lost")
	}

	resumed := make(chan awaitstore.Condition, 2)
	loop := NewLoopEngine()
	loop.SetWaitStore(store)
	loop.SetDeps(LoopDeps{
		GetSession: func(context.Context, string) (*api.Session, error) {
			return &api.Session{ID: "direct-1", AgentType: "explore", Status: api.SessionStatusIdle}, nil
		},
		Limits:               func(context.Context, *api.Session) settings.SessionLimits { return settings.DefaultSessionLimits() },
		IsCoordinatorSession: func(context.Context, *api.Session) bool { return false },
		RunWaitResume: func(_ context.Context, sessionID string, delivery WaitDelivery) (*promptresult.Result, error) {
			leaseID, got, onAdmitted := delivery.LeaseID, delivery.Condition, delivery.Admitted
			if sessionID != "direct-1" || leaseID != lease.ID {
				t.Errorf("resume target = %q/%q, want direct-1/%s", sessionID, leaseID, lease.ID)
			}
			if err := onAdmitted(); err != nil {
				return nil, err
			}
			resumed <- got
			return &promptresult.Result{}, nil
		},
	})
	if err := RecoverWaitLeases(context.Background(), loop, store); err != nil {
		t.Fatalf("recover settled direct wait: %v", err)
	}
	select {
	case got := <-resumed:
		if got.Kind != winner.Kind || got.Outcome != winner.Outcome {
			t.Fatalf("resume winner = %+v, want %+v", got, winner)
		}
	case <-time.After(time.Second):
		t.Fatal("settled direct wait did not resume")
	}
	loop.WaitForAsyncTurns(testutil.BoundedContext(t, time.Second))
	if pending, err := store.PendingAgentResumes(t.Context()); err != nil || len(pending) != 0 {
		t.Fatalf("pending resumes after delivery = %+v err=%v", pending, err)
	}
	if err := RecoverWaitLeases(context.Background(), loop, store); err != nil {
		t.Fatalf("recover delivered direct wait: %v", err)
	}
	select {
	case got := <-resumed:
		t.Fatalf("delivered direct wait resumed twice: %+v", got)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestSettledWaitRetriesTransientAdmissionFailure(t *testing.T) {
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertSession(t, database, "direct-retry", testdbseed.DefaultProjectID)
	store := &awaitstore.Store{DB: database}
	lease, err := store.Arm(t.Context(), awaitstore.Lease{
		SessionID: "direct-retry", ProjectID: testdbseed.DefaultProjectID, ToolCallID: "call-retry",
		ProfileID: "explore_readonly", Deadline: time.Now().UTC().Add(time.Minute),
		Conditions: []awaitstore.Condition{{Kind: "port_ready", Host: "localhost", Port: 8080}},
	})
	testutil.FailErr(t, "arm retry wait", err)
	winner := awaitstore.Condition{Kind: "port_ready", Outcome: "satisfied", Host: "localhost", Port: 8080}
	won, err := store.SettleLease(t.Context(), lease.ID, "resolved", winner)
	testutil.FailErr(t, "settle retry wait", err)
	if !won {
		t.Fatal("retry wait settlement lost")
	}

	resumed := make(chan struct{}, 1)
	var attempts atomic.Int32
	loop := NewLoopEngine()
	loop.SetWaitStore(store)
	loop.SetDeps(LoopDeps{
		GetSession: func(context.Context, string) (*api.Session, error) {
			return &api.Session{ID: "direct-retry", AgentType: "explore", Status: api.SessionStatusIdle}, nil
		},
		Limits:               func(context.Context, *api.Session) settings.SessionLimits { return settings.DefaultSessionLimits() },
		IsCoordinatorSession: func(context.Context, *api.Session) bool { return false },
		RunWaitResume: func(_ context.Context, _ string, delivery WaitDelivery) (*promptresult.Result, error) {
			onAdmitted := delivery.Admitted
			if attempts.Add(1) == 1 {
				return nil, errors.New("transient admission failure")
			}
			if err := onAdmitted(); err != nil {
				return nil, err
			}
			resumed <- struct{}{}
			return &promptresult.Result{}, nil
		},
	})
	t.Cleanup(func() { loop.ForgetSession(context.Background(), "direct-retry") })
	if err := RecoverWaitLeases(context.Background(), loop, store); err != nil {
		t.Fatalf("recover retry wait: %v", err)
	}
	select {
	case <-resumed:
	case <-time.After(3 * time.Second):
		t.Fatalf("wait resume attempts = %d, want successful retry", attempts.Load())
	}
	if got := attempts.Load(); got != 2 {
		t.Fatalf("wait resume attempts = %d, want 2", got)
	}
	if pending, err := store.PendingAgentResumes(t.Context()); err != nil || len(pending) != 0 {
		t.Fatalf("pending resumes after retry = %+v err=%v", pending, err)
	}
}
