package board_test

import (
	"context"
	repotest "github.com/lycaon/lycaon/internal/testsetup/repoinfo"
	"testing"

	"github.com/lycaon/lycaon/internal/board"
	"github.com/lycaon/lycaon/internal/packboard"
	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSnapshotBuilderIncludesHostLine(t *testing.T) {
	t.Cleanup(func() { platform.SetBoardHostOverride(nil) })
	platform.SetBoardHostOverride(&api.BoardHostSlice{
		OS:              "windows",
		Arch:            "amd64",
		ExecutionTarget: api.ExecutionTargetLocal,
		Shell:           true,
	})
	b := &board.SnapshotBuilder{
		Repo:                   repotest.NewProvider(t),
		DefaultExecutionTarget: api.ExecutionTargetLocal,
	}
	snap, err := b.Build(context.Background(), testdbseed.DefaultProjectID, t.TempDir(), "sess-1", api.BoardDetailLevelCompact, nil)
	testutil.FailErr(t, "SnapshotBuilder.Build", err)
	if snap.Host == nil || snap.Host.OS != "windows" {
		t.Fatalf("host = %+v", snap.Host)
	}
	lines := packboard.BuildOrientationLines(*snap, packboard.OrientOpts{})
	for _, line := range lines {
		if line == "Host: windows/amd64 · local sidecar · shell" {
			return
		}
	}
	t.Fatalf("orientation lines = %v", lines)
}
