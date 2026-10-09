package loopwake

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestWaitCompletionEndsCycleUsesHostLifecycle(t *testing.T) {
	parked := &api.ToolCompletion{Operation: "wait", State: "parked"}
	for _, tc := range []struct {
		name       string
		tool       string
		completion *api.ToolCompletion
		want       bool
	}{
		{name: "parked wait", tool: "wait", completion: parked, want: true},
		{name: "case normalized", tool: " WAIT ", completion: parked, want: true},
		{name: "missing completion", tool: "wait"},
		{name: "wrong tool", tool: "task", completion: parked},
		{name: "wrong operation", tool: "wait", completion: &api.ToolCompletion{Operation: "task", State: "parked"}},
		{name: "satisfied", tool: "wait", completion: &api.ToolCompletion{Operation: "wait", State: "satisfied"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := WaitCompletionEndsCycle(tc.tool, tc.completion); got != tc.want {
				t.Fatalf("WaitCompletionEndsCycle() = %v want %v", got, tc.want)
			}
		})
	}
}

func TestAskUserEndsCycleUsesStructuredStatus(t *testing.T) {
	decorated := "{\"phase_id\":\"ask-1\",\"status\":\"pending\"}\n\n>>> Tool feedback\nstatus is answered"
	if !AskUserEndsCycle("ask_user", decorated, true) {
		t.Fatal("pending ask must end the cycle")
	}
	if AskUserEndsCycle("ask_user", `{"detail":"status is pending"}`, true) {
		t.Fatal("status prose must not end the cycle")
	}
	if AskUserEndsCycle("ask_user", `{"status":"answered"}`, true) {
		t.Fatal("answered ask must not end the cycle")
	}
	if AskUserEndsCycle("ask_user", `{"status":"pending"}`, false) {
		t.Fatal("failed ask must not end the cycle")
	}
}

func TestRegisterWaitToolParksWithHostCompletion(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	loop := NewLoopEngine()
	t.Cleanup(func() { loop.ForgetSession(context.Background(), "s1") })
	loop.SetDeps(busyWaitLoopDeps())
	if err := RegisterWaitTool(reg, loop, WaitToolDeps{}); err != nil {
		t.Fatalf("RegisterWaitTool: %v", err)
	}
	outcome := &tools.ToolInvocationOut{}
	out, err := reg.Run(context.Background(), "wait", map[string]any{
		"timeout_ms": 300_000,
		"reason":     "scouts running",
	}, tools.ToolContext{SessionID: "s1", Agent: orchestration.ProfileCoordinator, Out: outcome})
	if err != nil {
		t.Fatalf("wait tool: %v", err)
	}
	var result WaitToolResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("unmarshal wait result: %v", err)
	}
	if result.Status != "parked" || result.TimeoutMS != 300_000 {
		t.Fatalf("result = %#v", result)
	}
	if !WaitCompletionEndsCycle("wait", outcome.Completion) {
		t.Fatalf("completion = %#v", outcome.Completion)
	}
	if !loop.IsSleeping("s1") {
		t.Fatal("expected wait to arm sleep")
	}
	if got := loop.WaitSubscriptionForTest("s1"); !hasWaitTrigger(got, WaitTriggerTimer) {
		t.Fatalf("triggers = %v want timer", got)
	}
}

func TestWaitParksEveryPublishedAgentRole(t *testing.T) {
	profiles := []string{"coordinator", "implement", "editor_file", "explore_readonly", "worker_readonly", "plan_review_readonly", "plan_write_only", "web_research"}
	allowed := make(map[string]map[string]bool, len(profiles))
	for _, profile := range profiles {
		allowed[profile] = map[string]bool{"port_ready": true}
	}
	registry := tools.NewDefaultRegistry()
	loop := NewLoopEngine()
	t.Cleanup(func() { loop.ForgetSession(context.Background(), "s1") })
	loop.SetDeps(busyWaitLoopDeps())
	if err := RegisterWaitTool(registry, loop, WaitToolDeps{ProfileConditions: allowed}); err != nil {
		t.Fatalf("RegisterWaitTool: %v", err)
	}
	for i, profile := range profiles {
		sessionID := profile + "-session"
		t.Cleanup(func() { loop.ForgetSession(context.Background(), sessionID) })
		outcome := &tools.ToolInvocationOut{}
		_, err := registry.Run(t.Context(), "wait", map[string]any{
			"timeout_ms": 1_000,
			"conditions": []any{map[string]any{"kind": "port_ready", "host": "localhost", "port": 19000 + i}},
		}, tools.ToolContext{
			SessionID: sessionID, Agent: profile, Out: outcome,
			LoopbackConnectGranted: true, LoopbackConnectPorts: []uint16{uint16(19000 + i)},
		})
		if err != nil {
			t.Fatalf("profile %s wait: %v", profile, err)
		}
		if !WaitCompletionEndsCycle("wait", outcome.Completion) || !loop.IsSleeping(sessionID) {
			t.Fatalf("profile %s did not park", profile)
		}
		loop.InterruptSleep(t.Context(), sessionID)
	}
}

func TestWaitRejectsConditionShapeWithStructuredCode(t *testing.T) {
	registry := tools.NewDefaultRegistry()
	loop := NewLoopEngine()
	t.Cleanup(func() { loop.ForgetSession(context.Background(), "s1") })
	loop.SetDeps(busyWaitLoopDeps())
	if err := RegisterWaitTool(registry, loop, WaitToolDeps{}); err != nil {
		t.Fatalf("RegisterWaitTool: %v", err)
	}
	_, err := registry.Run(t.Context(), "wait", map[string]any{
		"conditions": []any{map[string]any{"kind": "next_worker_done", "url": "https://example.test"}},
	}, tools.ToolContext{SessionID: "s1", Agent: orchestration.ProfileCoordinator})
	reject := tools.AsToolReject(err)
	if reject == nil || reject.Code != "TOOL_ARGS_INVALID" {
		t.Fatalf("error = %#v, want TOOL_ARGS_INVALID", err)
	}
}

func TestWaitResumeRearmsInterruptedSleep(t *testing.T) {
	loop := NewLoopEngine()
	t.Cleanup(func() { loop.ForgetSession(context.Background(), "s1") })
	loop.SetDeps(busyWaitLoopDeps())
	interrupted := time.Now().UTC().Add(7 * time.Minute)
	loop.EnterSleep(context.Background(), "s1", interrupted, "waiting for workers", DefaultCoordinatorWaitTriggers(false), nil, SleepMoverHost)
	loop.breakSleep(t.Context(), "s1", "worker_task_finished", true)

	reg := tools.NewDefaultRegistry()
	if err := RegisterWaitTool(reg, loop, WaitToolDeps{}); err != nil {
		t.Fatalf("RegisterWaitTool: %v", err)
	}
	out, err := reg.Run(context.Background(), "wait", map[string]any{
		"resume": true,
		"reason": "siblings in flight",
	}, tools.ToolContext{SessionID: "s1", Agent: orchestration.ProfileCoordinator})
	if err != nil {
		t.Fatalf("wait resume: %v", err)
	}
	var result WaitToolResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("unmarshal wait result: %v", err)
	}
	if !result.Resumed || result.TimeoutMS != 0 {
		t.Fatalf("result = %#v", result)
	}
	wakeAt, err := time.Parse(time.RFC3339, result.WakeAt)
	if err != nil {
		t.Fatalf("parse wake_at: %v", err)
	}
	if !wakeAt.UTC().Truncate(time.Second).Equal(interrupted.UTC().Truncate(time.Second)) {
		t.Fatalf("wake_at = %v want %v", wakeAt, interrupted)
	}
	if !loop.InterruptedUntilForTest("s1").IsZero() {
		t.Fatal("resume must consume the interrupted deadline")
	}
}

func TestWaitResumeUsesExplicitConditions(t *testing.T) {
	loop := NewLoopEngine()
	t.Cleanup(func() { loop.ForgetSession(context.Background(), "s1") })
	loop.SetDeps(busyWaitLoopDeps())
	reg := tools.NewDefaultRegistry()
	if err := RegisterWaitTool(reg, loop, WaitToolDeps{}); err != nil {
		t.Fatalf("RegisterWaitTool: %v", err)
	}
	outcome := &tools.ToolInvocationOut{}
	out, err := reg.Run(context.Background(), "wait", map[string]any{
		"resume": true,
		"conditions": []any{
			map[string]any{"kind": "all_workers_idle"},
		},
	}, tools.ToolContext{SessionID: "s1", Agent: orchestration.ProfileCoordinator, Out: outcome})
	if err != nil {
		t.Fatalf("wait resume with conditions: %v", err)
	}
	if !WaitCompletionEndsCycle("wait", outcome.Completion) {
		t.Fatalf("output = %q completion = %#v", out, outcome.Completion)
	}
	triggers := loop.WaitSubscriptionForTest("s1")
	if !hasWaitTrigger(triggers, WaitTriggerAllWorkersIdle) || !hasWaitTrigger(triggers, WaitTriggerTimer) {
		t.Fatalf("triggers = %v want all_workers_idle and timer", triggers)
	}
	if hasWaitTrigger(triggers, WaitTriggerNextWorkerDone) {
		t.Fatalf("explicit conditions must replace interrupted subscriptions: %v", triggers)
	}
}

func TestWaitResumeFallsBackWithoutInterruptedSleep(t *testing.T) {
	loop := NewLoopEngine()
	t.Cleanup(func() { loop.ForgetSession(context.Background(), "s1") })
	loop.SetDeps(busyWaitLoopDeps())
	reg := tools.NewDefaultRegistry()
	if err := RegisterWaitTool(reg, loop, WaitToolDeps{}); err != nil {
		t.Fatalf("RegisterWaitTool: %v", err)
	}
	before := time.Now().UTC()
	out, err := reg.Run(context.Background(), "wait", map[string]any{"resume": true, "timeout_ms": 300_000}, tools.ToolContext{
		SessionID: "s1", Agent: orchestration.ProfileCoordinator,
	})
	if err != nil {
		t.Fatalf("wait resume: %v", err)
	}
	var result WaitToolResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("unmarshal wait result: %v", err)
	}
	if result.Resumed {
		t.Fatalf("result = %#v want resumed=false", result)
	}
	wakeAt, err := time.Parse(time.RFC3339, result.WakeAt)
	if err != nil {
		t.Fatalf("parse wake_at: %v", err)
	}
	want := before.Add(5 * time.Minute)
	if wakeAt.Before(want.Add(-2*time.Second)) || wakeAt.After(want.Add(2*time.Second)) {
		t.Fatalf("wake_at = %v want approximately %v", wakeAt, want)
	}
}

func TestUserPromptClearsInterruptedSleepAndPendingWake(t *testing.T) {
	loop := NewLoopEngine()
	t.Cleanup(func() { loop.ForgetSession(context.Background(), "s1") })
	interrupted := time.Now().UTC().Add(9 * time.Minute)
	loop.enqueuePending("s1", pendingLoopWake{wake: anchor.WaitTimerFired, seq: loop.nudgeSeq.Add(1)})
	loop.EnterSleep(context.Background(), "s1", interrupted, "waiting for workers", DefaultCoordinatorWaitTriggers(false), nil, SleepMoverHost)
	loop.InterruptSleep(t.Context(), "s1")
	if _, ok := loop.PendingForTest("s1"); ok {
		t.Fatal("user interrupt must clear pending loop nudges")
	}
	if !loop.InterruptedUntilForTest("s1").IsZero() {
		t.Fatal("user interrupt must clear interrupted deadline")
	}
}

func TestBreakSleepStashesDeadlineOnlyForNudge(t *testing.T) {
	loop := NewLoopEngine()
	t.Cleanup(func() { loop.ForgetSession(context.Background(), "s1") })
	deadline := time.Now().UTC().Add(4 * time.Minute)
	loop.EnterSleep(context.Background(), "s1", deadline, "host cycle complete", DefaultCoordinatorWaitTriggers(false), nil, SleepMoverHost)
	loop.breakSleep(t.Context(), "s1", "user_prompt", false)
	if !loop.InterruptedUntilForTest("s1").IsZero() {
		t.Fatal("user prompt must not stash a deadline")
	}
	loop.EnterSleep(context.Background(), "s1", deadline, "host cycle complete", DefaultCoordinatorWaitTriggers(false), nil, SleepMoverHost)
	loop.breakSleep(t.Context(), "s1", "worker_task_finished", true)
	if got := loop.InterruptedUntilForTest("s1"); !got.Equal(deadline) {
		t.Fatalf("stashed deadline = %v want %v", got, deadline)
	}
}

func TestCapWaitDuration(t *testing.T) {
	maxWait := 3 * time.Hour
	for _, tc := range []struct {
		requested time.Duration
		want      time.Duration
	}{
		{requested: 0, want: DefaultWaitSeconds * time.Second},
		{requested: 5 * time.Second, want: 5 * time.Second},
		{requested: time.Millisecond, want: MinWaitSeconds * time.Second},
		{requested: 2 * time.Hour, want: 2 * time.Hour},
		{requested: 4 * time.Hour, want: maxWait},
	} {
		if got := CapWaitDuration(tc.requested, maxWait); got != tc.want {
			t.Fatalf("CapWaitDuration(%v) = %v want %v", tc.requested, got, tc.want)
		}
	}
}

func TestParseWaitResumeArg(t *testing.T) {
	for _, tc := range []struct {
		in   any
		want bool
	}{
		{in: true, want: true},
		{in: false},
		{in: float64(1)},
		{in: float64(0)},
		{in: "true"},
	} {
		if got := parseWaitResumeArg(tc.in); got != tc.want {
			t.Fatalf("parseWaitResumeArg(%#v) = %v want %v", tc.in, got, tc.want)
		}
	}
}

func TestWaitAllWorkersIdleAlreadySatisfied(t *testing.T) {
	loop := NewLoopEngine()
	t.Cleanup(func() { loop.ForgetSession(context.Background(), "s1") })
	loop.SetDeps(idleWaitLoopDeps())
	reg := tools.NewDefaultRegistry()
	if err := RegisterWaitTool(reg, loop, WaitToolDeps{}); err != nil {
		t.Fatalf("RegisterWaitTool: %v", err)
	}
	outcome := &tools.ToolInvocationOut{}
	out, err := reg.Run(context.Background(), "wait", map[string]any{
		"conditions": []any{map[string]any{"kind": "all_workers_idle"}},
	}, tools.ToolContext{SessionID: "s1", Agent: orchestration.ProfileCoordinator, Out: outcome})
	if err != nil {
		t.Fatalf("wait all_workers_idle: %v", err)
	}
	var result WaitToolResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("unmarshal wait result: %v", err)
	}
	if result.Status != "all_workers_idle" || loop.IsSleeping("s1") {
		t.Fatalf("result = %#v sleeping = %v", result, loop.IsSleeping("s1"))
	}
	if WaitCompletionEndsCycle("wait", outcome.Completion) {
		t.Fatal("already-satisfied condition must keep the cycle active")
	}
}

func TestWaitTimerOnlyParksWhenCycleIdle(t *testing.T) {
	loop := NewLoopEngine()
	t.Cleanup(func() { loop.ForgetSession(context.Background(), "s1") })
	loop.SetDeps(idleWaitLoopDeps())
	reg := tools.NewDefaultRegistry()
	if err := RegisterWaitTool(reg, loop, WaitToolDeps{}); err != nil {
		t.Fatalf("RegisterWaitTool: %v", err)
	}
	outcome := &tools.ToolInvocationOut{}
	out, err := reg.Run(context.Background(), "wait", map[string]any{
		"timeout_ms": 5_000,
	}, tools.ToolContext{SessionID: "s1", Agent: orchestration.ProfileCoordinator, Out: outcome})
	if err != nil {
		t.Fatalf("timer wait: %v", err)
	}
	if !WaitCompletionEndsCycle("wait", outcome.Completion) || !loop.IsSleeping("s1") {
		t.Fatalf("output = %q completion = %#v sleeping = %v", out, outcome.Completion, loop.IsSleeping("s1"))
	}
}

func TestWaitParksWhileUserInputPending(t *testing.T) {
	loop := NewLoopEngine()
	t.Cleanup(func() { loop.ForgetSession(context.Background(), "s1") })
	loop.SetDeps(pendingAskIdleWaitLoopDeps())
	reg := tools.NewDefaultRegistry()
	if err := RegisterWaitTool(reg, loop, WaitToolDeps{}); err != nil {
		t.Fatalf("RegisterWaitTool: %v", err)
	}
	outcome := &tools.ToolInvocationOut{}
	out, err := reg.Run(context.Background(), "wait", map[string]any{"resume": true}, tools.ToolContext{
		SessionID: "s1", Agent: orchestration.ProfileCoordinator, Out: outcome,
	})
	if err != nil {
		t.Fatalf("wait with pending user input: %v", err)
	}
	var result WaitToolResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("unmarshal wait result: %v", err)
	}
	if result.Status != "parked" || !WaitCompletionEndsCycle("wait", outcome.Completion) || !loop.IsSleeping("s1") {
		t.Fatalf("result = %#v completion = %#v sleeping = %v", result, outcome.Completion, loop.IsSleeping("s1"))
	}
	deadline, err := time.Parse(time.RFC3339, result.WakeAt)
	if err != nil {
		t.Fatalf("parse wake_at: %v", err)
	}
	if !deadline.After(time.Now().UTC().Add(30 * time.Minute)) {
		t.Fatalf("wake_at = %v want pending-input backstop", deadline)
	}
}

func hasWaitTrigger(triggers []WaitTrigger, want WaitTrigger) bool {
	for _, trigger := range triggers {
		if trigger == want {
			return true
		}
	}
	return false
}

func idleWaitLoopDeps() LoopDeps {
	deps := loopDepsForTest()
	deps.GetSession = func(context.Context, string) (*api.Session, error) {
		return &api.Session{ID: "s1", Status: api.SessionStatusIdle}, nil
	}
	return deps
}

func busyWaitLoopDeps() LoopDeps {
	deps := idleWaitLoopDeps()
	deps.GetSession = func(context.Context, string) (*api.Session, error) {
		return &api.Session{ID: "s1", Status: api.SessionStatusBusy}, nil
	}
	deps.WorkerCycleIdle = func(context.Context, *api.Session, string) (bool, error) { return false, nil }
	return deps
}

func pendingAskIdleWaitLoopDeps() LoopDeps {
	deps := idleWaitLoopDeps()
	deps.WorkflowSource = workflowFixturePorts(StubLoopWF{
		run: &api.WorkflowRun{ID: "run-1", SessionID: "s1", Status: api.WorkflowRunStatusRunning},
		vars: map[string]any{"user_feedback": map[string]any{
			"ask-1": map[string]any{"pending": true, "source": "coordinator_tool", "blocking": true},
		}},
	})
	return deps
}
