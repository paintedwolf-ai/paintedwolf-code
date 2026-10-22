package contract

import (
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestLinuxCandidateRequestsCannotReplaceReleaseOrMaintenance(t *testing.T) {
	t.Parallel()
	plan := loadCapacityPlan(t)
	builder := plan.workflows["linux-candidates.yml"]
	if _, ok := builder.On["workflow_call"]; !ok || len(builder.On) != 1 || builder.Concurrency.Kind != 0 {
		t.Fatal("package construction must inherit its caller's capacity without entering another queue")
	}
	requests := plan.workflows["linux-candidate-requests.yml"]
	for _, event := range []string{"workflow_dispatch", "schedule"} {
		if _, ok := requests.On[event]; !ok {
			t.Errorf("candidate requests omit %s", event)
		}
	}
	if len(requests.On) != 2 || len(requests.Jobs) != 1 {
		t.Fatal("standalone package requests must be one scheduled or manual build")
	}
	var concurrency struct {
		Group  string
		Queue  string
		Cancel bool `yaml:"cancel-in-progress"`
	}
	contractcheck.FailErr(t, "decode candidate request concurrency", requests.Concurrency.Decode(&concurrency))
	if concurrency.Group != "linux-candidate-requests" || concurrency.Queue != "max" || concurrency.Cancel {
		t.Fatal("standalone candidates must retain pending requests in their own bounded queue")
	}
	for _, workflow := range []string{"release.yml", "queue-health.yml"} {
		if plan.group(t, workflow) == concurrency.Group {
			t.Errorf("%s shares the standalone candidate queue", workflow)
		}
	}
	call := requests.Jobs["candidates"]
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
