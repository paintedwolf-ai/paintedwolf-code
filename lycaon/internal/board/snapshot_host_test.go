package board_test

import (
	"context"
	"fmt"
	"runtime"
	"testing"

	"github.com/lycaon/lycaon/internal/board"
	"github.com/lycaon/lycaon/internal/packboard"
	"github.com/lycaon/lycaon/internal/testdbseed"
	repotest "github.com/lycaon/lycaon/internal/testsetup/repoinfo"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSnapshotBuilderIncludesHostLine(t *testing.T) {
	b := &board.SnapshotBuilder{
		Repo:                   repotest.NewProvider(t),
		DefaultExecutionTarget: api.ExecutionTargetLocal,
	}
	snap, err := b.Build(context.Background(), testdbseed.DefaultProjectID, t.TempDir(), "sess-1", api.BoardDetailLevelCompact, nil)
	testutil.FailErr(t, "SnapshotBuilder.Build", err)
	if snap.Host == nil || snap.Host.OS != runtime.GOOS || snap.Host.Arch != runtime.GOARCH {
		t.Fatalf("host = %+v", snap.Host)
	}
	lines := packboard.BuildOrientationLines(*snap, packboard.OrientOpts{})
	for _, line := range lines {
		if line == fmt.Sprintf("Host: %s/%s · local sidecar · shell", runtime.GOOS, runtime.GOARCH) {
			return
		}
	}
	t.Fatalf("orientation lines = %v", lines)
}
