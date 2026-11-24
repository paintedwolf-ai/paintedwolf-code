package kick

import (
	"slices"
	"testing"
)

func TestKicksForDifferentSubjectsQueueSeparately(t *testing.T) {
	engine := &stubEngine{render: func(name string, data map[string]any) (string, error) {
		return name + ":" + data["worker"].(string), nil
	}}
	kicks := &KickEngine{}
	kicks.SetPromptEngine(engine)
	for _, job := range []string{"job-a", "job-b", "job-a"} {
		kicks.QueueDeferred("s1", "coordinator-worker-round-notice",
			WithSubject(job), WithWorkerRoundNotice(WorkerRoundNoticeFacts{JobID: job, Worker: job}))
	}
	var rendered []string
	for kicks.TakePendingKickIDUnless("s1", nil) != "" {
		text, lease, ok, err := kicks.RenderPendingNudge(t.Context(), "s1", CoordinatorKickRenderContext{})
		if err != nil || !ok {
			t.Fatalf("render = %v %v", ok, err)
		}
		rendered = append(rendered, text)
		kicks.AckPendingNudge("s1", lease)
	}
	want := []string{"coordinator-worker-round-notice:job-a", "coordinator-worker-round-notice:job-b"}
	if !slices.Equal(rendered, want) {
		t.Fatalf("rendered = %v, want one kick per job in arrival order %v", rendered, want)
	}
}

func TestSubjectlessKickCoversEverySubject(t *testing.T) {
	kicks := &KickEngine{}
	kicks.QueueDeferred("s1", "coordinator-worker-task-finished")
	kicks.QueueDeferred("s1", "coordinator-worker-task-finished", WithSubject("job-a"))
	kicks.QueueDeferred("s1", "coordinator-leg-finished", WithSubject("leg-1"))
	got := kicks.PendingKickIDsUnless("s1", nil)
	want := []string{"coordinator-worker-task-finished", "coordinator-leg-finished"}
	if !slices.Equal(got, want) {
		t.Fatalf("pending = %v, want the cycle kick to carry job-a's finish: %v", got, want)
	}
}

func TestPendingKickIDsDropsClearedSubjects(t *testing.T) {
	kicks := &KickEngine{}
	kicks.QueueDeferred("s1", "coordinator-worker-task-finished", WithSubject("job-a"))
	kicks.QueueDeferred("s1", "coordinator-worker-budget-requested", WithSubject("job-a"))
	kicks.QueueDeferred("s1", "coordinator-worker-budget-requested", WithSubject("job-b"))
	answered := func(kickID, subject string) bool {
		return kickID == "coordinator-worker-budget-requested" && subject == "job-a"
	}
	want := []string{"coordinator-worker-task-finished", "coordinator-worker-budget-requested"}
	if got := kicks.PendingKickIDsUnless("s1", answered); !slices.Equal(got, want) {
		t.Fatalf("pending = %v, want %v", got, want)
	}
	if !kicks.HasQueuedKick("s1", "coordinator-worker-budget-requested") {
		t.Fatal("job-b's open request was dropped with job-a's answered one")
	}
}

func TestProcessFinishedKicksQueuePerHandleAndSurviveClearPendingOnTurnAbort(t *testing.T) {
	engine := &stubEngine{render: func(name string, data map[string]any) (string, error) {
		return name + ":" + data["command_completion"].(string), nil
	}}
	kicks := &KickEngine{}
	kicks.SetPromptEngine(engine)

	kicks.QueueDeferred("s1", "coordinator-gate-blocked")
	kicks.QueueDeferred("s1", "coordinator-process-finished",
		WithSubject("bg-1"),
		WithCommandCompletion("handle=bg-1 termination=stopped_by_user\ncommand=npm run dev"),
	)
	kicks.QueueDeferred("s1", "coordinator-process-finished",
		WithSubject("bg-2"),
		WithCommandCompletion("handle=bg-2 termination=stopped_by_user\ncommand=tail -f app.log"),
	)

	// Stage the first kick, then abort the turn (ClearPending).
	_ = kicks.TakePendingKickIDUnless("s1", nil)
	kicks.ClearPending("s1")

	var rendered []string
	for kicks.TakePendingKickIDUnless("s1", nil) != "" {
		text, lease, ok, err := kicks.RenderPendingNudge(t.Context(), "s1", CoordinatorKickRenderContext{})
		if err != nil || !ok {
			t.Fatalf("render = %v %v", ok, err)
		}
		rendered = append(rendered, text)
		kicks.AckPendingNudge("s1", lease)
	}
	want := []string{
		"coordinator-process-finished:handle=bg-1 termination=stopped_by_user\ncommand=npm run dev",
		"coordinator-process-finished:handle=bg-2 termination=stopped_by_user\ncommand=tail -f app.log",
	}
	if !slices.Equal(rendered, want) {
		t.Fatalf("rendered after ClearPending = %v, want %v", rendered, want)
	}
}

func TestDropKickSubjectAndAckReturnsSubject(t *testing.T) {
	var nilKicks *KickEngine
	if nilKicks.DropKickSubject("s1", "coordinator-process-finished", "bg-1") {
		t.Fatal("nil KickEngine reported dropped subject")
	}
	if got := nilKicks.AckPendingNudge("s1", 1); got != "" {
		t.Fatalf("nil KickEngine AckPendingNudge = %q, want empty", got)
	}

	engine := &stubEngine{render: func(name string, data map[string]any) (string, error) {
		return name + ":" + data["command_completion"].(string), nil
	}}
	kicks := &KickEngine{}
	kicks.SetPromptEngine(engine)

	if kicks.DropKickSubject("", "coordinator-process-finished", "bg-1") ||
		kicks.DropKickSubject("s1", "", "bg-1") ||
		kicks.DropKickSubject("s1", "coordinator-process-finished", "") {
		t.Fatal("blank arguments reported dropped subject")
	}
	if got := kicks.AckPendingNudge("s1", 0); got != "" {
		t.Fatalf("zero lease AckPendingNudge = %q, want empty", got)
	}

	kicks.QueueDeferred("s1", "coordinator-process-finished",
		WithSubject("bg-1"), WithCommandCompletion("handle=bg-1"))
	kicks.QueueDeferred("s1", "coordinator-process-finished",
		WithSubject("bg-2"), WithCommandCompletion("handle=bg-2"))
	kicks.QueueDeferred("s1", "coordinator-process-finished",
		WithSubject("bg-3"), WithCommandCompletion("handle=bg-3"))

	if !kicks.DropKickSubject("s1", "coordinator-process-finished", "bg-1") {
		t.Fatal("expected queued bg-1 kick to drop")
	}
	if kicks.DropKickSubject("s1", "coordinator-process-finished", "bg-missing") {
		t.Fatal("unexpected drop for missing subject")
	}

	_ = kicks.TakePendingKickIDUnless("s1", nil)
	if !kicks.DropKickSubject("s1", "coordinator-process-finished", "bg-2") {
		t.Fatal("expected staged bg-2 kick to drop")
	}

	_ = kicks.TakePendingKickIDUnless("s1", nil)
	_, lease, ok, err := kicks.RenderPendingNudge(t.Context(), "s1", CoordinatorKickRenderContext{})
	if err != nil || !ok {
		t.Fatalf("render bg-3 = (%v, %v)", ok, err)
	}
	if got := kicks.AckPendingNudge("s1", lease+99); got != "" {
		t.Fatalf("mismatched lease AckPendingNudge = %q, want empty", got)
	}
	if got := kicks.AckPendingNudge("s1", lease); got != "bg-3" {
		t.Fatalf("AckPendingNudge subject = %q, want bg-3", got)
	}
}
