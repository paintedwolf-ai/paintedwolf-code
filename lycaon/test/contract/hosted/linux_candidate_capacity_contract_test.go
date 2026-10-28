package contract

import (
	"slices"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestLinuxCandidateRequestsShareMaintenanceWithoutBlockingRelease(t *testing.T) {
	t.Parallel()
	plan := loadCapacityPlan(t)
	builder := plan.workflows["linux-candidates.yml"]
	if _, ok := builder.On["workflow_call"]; !ok || len(builder.On) != 1 || builder.Concurrency.Kind != 0 {
		t.Fatal("package construction must inherit its caller's capacity without entering another queue")
	}
	requests := plan.workflows["linux-candidate-requests.yml"]
	for _, event := range []string{"workflow_dispatch", "schedule", "pull_request"} {
		if _, ok := requests.On[event]; !ok {
			t.Errorf("candidate requests omit %s", event)
		}
	}
	if len(requests.On) != 3 || len(requests.Jobs) != 1 {
		t.Fatal("standalone package requests must be one scheduled, manual, or packaging pull request build")
	}
	var pullRequest struct {
		Types    []string
		Branches []string
		Paths    []string
	}
	trigger := requests.On["pull_request"]
	contractcheck.FailErr(t, "decode candidate pull_request trigger", trigger.Decode(&pullRequest))
	if !slices.Equal(pullRequest.Branches, []string{"main"}) {
		t.Fatalf("packaging pull requests build candidates only when they target main, got branches %v: a stacked pull request cannot land and its build would hold the shared maintenance executor", pullRequest.Branches)
	}
	if !slices.Contains(pullRequest.Types, "ready_for_review") || len(pullRequest.Paths) == 0 {
		t.Fatal("packaging pull requests must build when marked ready and only when packaging inputs change")
	}
	var concurrency struct {
		Group  string
		Queue  string
		Cancel bool `yaml:"cancel-in-progress"`
	}
	contractcheck.FailErr(t, "decode candidate request concurrency", requests.Concurrency.Decode(&concurrency))
	if concurrency.Group != "maintenance" || concurrency.Queue != "" || concurrency.Cancel {
		t.Fatal("standalone candidates must share the maintenance runner budget")
	}
	for _, workflow := range []string{"release.yml", "release-halt.yml"} {
		if plan.group(t, workflow) == concurrency.Group {
			t.Errorf("%s shares the standalone candidate queue", workflow)
		}
	}
	if plan.group(t, "queue-health.yml") != concurrency.Group {
		t.Fatal("standalone package builds must not add another capacity class")
	}
	call := requests.Jobs["candidates"]
	if call.If != "${{ github.event_name != 'pull_request' || !github.event.pull_request.draft || github.event.action == 'ready_for_review' }}" {
		t.Fatal("packaging pull requests must wait until ready")
	}
	if call.Uses != "./.github/workflows/linux-candidates.yml" || call.With["source_sha"] != "${{ github.sha }}" {
		t.Fatal("candidate requests must pass the immutable dispatch source to the shared build")
	}
	if got := plan.runFootprint(t, "linux-candidate-requests.yml"); got != (footprint{total: 1}) {
		t.Fatalf("standalone candidate footprint: got %+v, want one Linux runner", got)
	}
	if plan.capacity.Footprints["release.yml"] != 2 || plan.capacity.Footprints["linux-candidate-requests.yml"] != 1 {
		t.Fatal("release and standalone construction must retain their declared two- and one-runner budgets")
	}
}
