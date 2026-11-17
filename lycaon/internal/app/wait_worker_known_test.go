package app

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/pkg/api"
)

type fakeWaitRecords struct {
	names  map[string]bool
	rounds map[string]bool
}

func (f fakeWaitRecords) Resolve(_ context.Context, _ workerrecords.Actor, name string) (workerrecords.Worker, error) {
	if f.names[name] {
		return workerrecords.Worker{}, nil
	}
	return workerrecords.Worker{}, errors.New("unknown worker")
}

func (f fakeWaitRecords) RoundByJob(_ context.Context, jobID string) (workerrecords.Round, bool, error) {
	return workerrecords.Round{}, f.rounds[jobID], nil
}

type fakeWaitJobs map[string]string

func (f fakeWaitJobs) Lookup(_ context.Context, id string) (*api.WorkerTask, bool) {
	parent, ok := f[id]
	if !ok {
		return nil, false
	}
	return &api.WorkerTask{ID: id, ParentSessionID: parent}, true
}

// A wait names a worker by its name; a raw job id is a handle only for a job that runs no worker round.
func TestWaitHandlesNameWorkersAndRoundlessJobs(t *testing.T) {
	known := waitWorkerKnown(
		fakeWaitRecords{names: map[string]bool{"ada": true}, rounds: map[string]bool{"job-ada": true}},
		fakeWaitJobs{"job-ada": "parent", "job-scan": "parent", "job-other": "other-parent"},
	)
	for handle, want := range map[string]bool{
		"ada":       true,
		"job-ada":   false,
		"job-scan":  true,
		"job-other": false,
		"nobody":    false,
	} {
		if got := known(t.Context(), "parent", handle); got != want {
			t.Errorf("handle %q known = %v, want %v", handle, got, want)
		}
	}
}
