package worker

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

type blockedJobs map[string]*api.WorkerTask

func (j blockedJobs) Get(id string) (*api.WorkerTask, bool) { t, ok := j[id]; return t, ok }

// Each waiting job whose prerequisite cannot be met is reported to its
// coordinator once, naming its producer and why.
func TestBlockedPrerequisitesNotifyOncePerJob(t *testing.T) {
	type notice struct{ parent, consumer, producer, reason string }
	var got []notice
	b := &BlockedPrerequisites{
		List: func(context.Context) ([]BlockedPrerequisite, error) {
			return []BlockedPrerequisite{{ConsumerJobID: "consumer", ProducerJobID: "producer", Reason: "closed"}}, nil
		},
		Jobs: blockedJobs{"consumer": {ID: "consumer", ParentSessionID: "coord", Status: api.WorkerStatusPending}},
		Notify: func(_ context.Context, parent, consumer, producer, reason string) {
			got = append(got, notice{parent, consumer, producer, reason})
		},
	}
	b.Check(t.Context())
	b.Check(t.Context())
	if len(got) != 1 || got[0] != (notice{"coord", "consumer", "producer", "closed"}) {
		t.Fatalf("notices = %+v", got)
	}
}
