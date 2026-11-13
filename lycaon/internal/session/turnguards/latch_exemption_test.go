package turnguards

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/pkg/api"
)

// Only an instruction to the worker whose latest result the latch holds is
// exempt; another worker, an unlatched result, a lookup failure or another
// tool stays under the latch.
func TestLatchExemptsOnlyTheLatchedWorker(t *testing.T) {
	latest := map[string]string{"fixer": "job-held", "scout": "job-other"}
	m := &Service{}
	m.SetWorkerResults(func(_ context.Context, sessionID, name string) (string, bool, error) {
		if sessionID != "coord" {
			t.Fatalf("resolved in session %q", sessionID)
		}
		if name == "broken" {
			return "", false, errors.New("store closed")
		}
		job, ok := latest[name]
		return job, ok, nil
	})
	sess := &api.Session{ID: "coord"}
	baseline := guard.ProgressClosureBaseline{JobIDs: []string{"job-held"}}
	for _, tc := range []struct {
		tool, worker string
		exempt       bool
	}{
		{"instruct_worker", "fixer", true},
		{"instruct_worker", "scout", false},
		{"instruct_worker", "nobody", false},
		{"instruct_worker", "broken", false},
		{"task", "fixer", false},
	} {
		got := m.instructsLatchedWorker(t.Context(), sess, tc.tool, map[string]any{"worker": tc.worker}, baseline)
		if got != tc.exempt {
			t.Fatalf("%s to %s: exempt=%v, want %v", tc.tool, tc.worker, got, tc.exempt)
		}
	}
}
