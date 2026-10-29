package contract

import (
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestQueueBatchingSerializesIndependently(t *testing.T) {
	t.Parallel()
	plan := loadCapacityPlan(t)
	workflow := plan.workflows["queue-batching.yml"]
	var concurrency struct {
		Group  string
		Cancel *bool `yaml:"cancel-in-progress"`
	}
	contractcheck.FailErr(t, "decode queue batching concurrency", workflow.Concurrency.Decode(&concurrency))
	if concurrency.Group != "queue-batching" || concurrency.Cancel == nil || *concurrency.Cancel {
		t.Fatal("queue batching must serialize in its own group and finish each started run")
	}
	for _, name := range plan.triggered() {
		if name != "queue-batching.yml" && plan.group(t, name) == concurrency.Group {
			t.Errorf("%s must not block queue batching", name)
		}
	}
}
