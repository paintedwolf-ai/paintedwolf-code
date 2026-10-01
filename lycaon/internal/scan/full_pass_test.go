package scan

import (
	"testing"
	"time"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestFullPassCompletionCannotPrecedeItsRequestOrStart(t *testing.T) {
	requested := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)
	for _, started := range []time.Time{{}, requested.Add(time.Second)} {
		for _, completed := range []time.Time{requested.Add(-time.Hour), requested.Add(2 * time.Second)} {
			pass := FullPass{RequestedAt: requested, StartedAt: started, Members: []FullPassMember{{Scan: &api.CodeScan{CompletedAt: &completed}}}}
			got := pass.completedAt()
			if got.Before(requested) || (!started.IsZero() && got.Before(started)) {
				t.Fatalf("completion %s precedes request %s or start %s", got, requested, started)
			}
			if completed.After(started) && completed.After(requested) && !got.Equal(completed) {
				t.Fatalf("completion = %s, want latest member %s", got, completed)
			}
		}
	}
}
