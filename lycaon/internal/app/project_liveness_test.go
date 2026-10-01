package app

import (
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestProjectLivenessAndParkingIntegration(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	t.Setenv("LYCAON_API_TOKEN", "test-liveness-token")

	cfg := testBuildConfig(t, configlayout.FindModuleRoot())
	serveApp, err := Build(t.Context(), cfg)
	testutil.FailErr(t, "Build failed", err)
	defer func() { _ = serveApp.Close() }()

	tracker := serveApp.ProjectLiveness
	if tracker == nil {
		t.Fatal("expected ProjectLiveness to be wired on serveApp")
	}

	ctx := t.Context()

	projectA := "fec103ae-9893-5dde-9112-4e9668066b5d"
	projectB := "c6bf4a44-9c4c-52c3-be7c-f1ace4a29adf"
	testdbseed.InsertProjectRoot(t, serveApp.DB, projectA, t.TempDir())
	testdbseed.InsertProjectRoot(t, serveApp.DB, projectB, t.TempDir())

	releaseA := tracker.ClaimWorkspace(projectA)
	if tracker.IsParked(projectA) {
		t.Fatal("project A should be active while workspace is claimed")
	}

	sess, err := serveApp.SessionStore.Create(ctx, api.CreateSessionRequest{
		ProjectID: projectA,
		Posture:   api.SessionPostureBuild,
	}, projectA)
	testutil.FailErr(t, "create session in project A", err)
	if sess == nil {
		t.Fatal("expected non-nil session")
	}

	releaseA()
	releaseB := tracker.ClaimWorkspace(projectB)

	projectC := "d303e0c6-c3d6-57ec-b58e-ced9bbd3fd6f"
	testdbseed.InsertProjectRoot(t, serveApp.DB, projectC, t.TempDir())

	releaseC := tracker.ClaimWorkspace(projectC)
	if tracker.IsParked(projectC) {
		t.Fatal("project C should be active while claimed")
	}
	releaseC()

	if !tracker.ParkNow(ctx, projectC) {
		t.Fatal("expected project C to park immediately via ParkNow when closed with no sessions")
	}
	if !tracker.IsParked(projectC) {
		t.Fatal("expected project C to be parked")
	}

	releaseC2 := tracker.ClaimWorkspace(projectC)
	if tracker.IsParked(projectC) {
		t.Fatal("expected project C to be active after re-claiming workspace")
	}
	releaseC2()
	releaseB()
}
