package assembly

import (
	"context"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type recordingBoardOrientReady struct {
	calls int
}

func (r *recordingBoardOrientReady) RecordBoardOrientReady(_ context.Context, _, _ string) error {
	r.calls++
	return nil
}

func TestPrepareCoordinatorBoardPreservesFrameRootAndPolicyFacts(t *testing.T) {
	frameSource := &advancingTurnFrameSource{phase: "boot", revision: 2}
	orient := &recordingBoardOrientReady{}
	assembler := turnContextAssembler{
		deps: turnContextDeps{
			CoordinatorFrame: frameSource,
			Board:            advancingBoard{},
			BoardOrientReady: orient,
		},
	}

	cases := []struct {
		name         string
		rootCount    int
		overlayRoots []string
		postureRules []string
	}{
		{
			name:         "rooted_session",
			rootCount:    2,
			overlayRoots: []string{"/repo-a", "/repo-b"},
			postureRules: []string{"posture/build.yaml"},
		},
		{
			name:         "rootless_session",
			rootCount:    0,
			overlayRoots: nil,
			postureRules: []string{"posture/build.yaml"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			orient.calls = 0
			sess := &api.Session{ID: "sess-" + tc.name, Posture: api.SessionPostureBuild}
			initial := inject.CoordinatorTurnFrame{
				WorkflowRevision: 1,
				RunContext:       api.CoordinatorRunContext{RunID: "run-1", CurrentPhase: "boot"},
				ProjectRootCount: tc.rootCount,
				OverlayRootPaths: append([]string(nil), tc.overlayRoots...),
				PostureRules:     append([]string(nil), tc.postureRules...),
				Machine:          inject.Machine{ProfileID: "coordinator"},
			}
			got, block, err := assembler.prepareCoordinatorBoard(t.Context(), sess, initial, &TurnAssemblyScratch{})
			testutil.FailErr(t, "prepareCoordinatorBoard", err)
			if orient.calls == 0 || block == "" {
				t.Fatalf("expected board orientation refresh to run, calls=%d block=%q", orient.calls, block)
			}
			if got.ProjectRootCount != tc.rootCount {
				t.Fatalf("ProjectRootCount = %d, want %d", got.ProjectRootCount, tc.rootCount)
			}
			if !slices.Equal(got.OverlayRootPaths, tc.overlayRoots) {
				t.Fatalf("OverlayRootPaths = %v, want %v", got.OverlayRootPaths, tc.overlayRoots)
			}
			if !slices.Equal(got.PostureRules, tc.postureRules) {
				t.Fatalf("PostureRules = %v, want %v", got.PostureRules, tc.postureRules)
			}
			if got.Machine.ProfileID != "coordinator" {
				t.Fatalf("Machine.ProfileID = %q, want coordinator", got.Machine.ProfileID)
			}
		})
	}
}
