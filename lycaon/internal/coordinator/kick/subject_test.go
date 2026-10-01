package kick

import (
	"slices"
	"testing"
)

func TestKicksForDifferentSubjectsQueueSeparately(t *testing.T) {
	engine := &stubEngine{render: func(name string, data map[string]any) (string, error) {
		return name + ":" + data["job_id"].(string), nil
	}}
	kicks := &KickEngine{}
	kicks.SetPromptEngine(engine)
	for _, job := range []string{"job-a", "job-b", "job-a"} {
		kicks.QueueDeferred("s1", "coordinator-worker-budget-requested",
			WithSubject(job), WithWorkerBudget(WorkerBudgetFacts{JobID: job}))
	}
	var rendered []string
	for kicks.TakePendingKickID("s1") != "" {
		text, lease, ok, err := kicks.RenderPendingNudge(t.Context(), "s1", CoordinatorKickRenderContext{})
		if err != nil || !ok {
			t.Fatalf("render = %v %v", ok, err)
		}
		rendered = append(rendered, text)
		kicks.AckPendingNudge("s1", lease)
	}
	want := []string{"coordinator-worker-budget-requested:job-a", "coordinator-worker-budget-requested:job-b"}
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
