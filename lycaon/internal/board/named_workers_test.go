package board

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testdbseed"
	repotest "github.com/lycaon/lycaon/internal/testsetup/repoinfo"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// The board view carries the session's named workers beside the job rows;
// the status level leaves both out, and a failed worker read fails the build.
func TestBoardViewCarriesNamedWorkers(t *testing.T) {
	rows := []api.BoardWorker{{Name: "fixer", AgentType: "implementer", State: api.WorkerStateDelivered, ResultHandle: "result-1"}}
	var readErr error
	b := &SnapshotBuilder{
		Repo: repotest.NewProvider(t),
		NamedWorkers: func(_ context.Context, sessionID string) ([]api.BoardWorker, error) {
			if sessionID != "sess-1" {
				t.Fatalf("named workers read for %q", sessionID)
			}
			return rows, readErr
		},
	}
	dir := t.TempDir()
	for level, want := range map[api.BoardDetailLevel]int{api.BoardDetailLevelCompact: 1, api.BoardDetailLevelStatus: 0} {
		snap, err := b.Build(t.Context(), testdbseed.DefaultProjectID, dir, "sess-1", level, nil)
		testutil.FailErr(t, "build", err)
		view := BuildBoardView(snap, "sess-1", level, time.Now().UTC())
		if len(view.Workers) != want || (want == 1 && view.Workers[0].ResultHandle != "result-1") {
			t.Fatalf("level %s: workers = %+v", level, view.Workers)
		}
	}
	readErr = errors.New("store closed")
	if _, err := b.Build(t.Context(), testdbseed.DefaultProjectID, dir, "sess-1", api.BoardDetailLevelCompact, nil); !errors.Is(err, readErr) {
		t.Fatalf("build error = %v", err)
	}
}
