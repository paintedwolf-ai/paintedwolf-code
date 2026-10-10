package sessioncontracts

import (
	"context"
	"testing"

	hostapi "github.com/lycaon/lycaon/internal/api"
	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/board"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestWarmRepoBriefDelegatesWithoutReading(t *testing.T) {
	p := &contractfixture.WarmTrackingRepo{}
	s := hostapi.NewServer(contractfixture.RequiredTestDeps(t, hostapi.Dependencies{Workflow: hostapi.WorkflowDependencies{Board: &board.SnapshotBuilder{Repo: p}}}), nil, hostapi.TestAPIToken)

	s.Admin.Git.WarmRepoBrief(" /project ")
	if len(p.Warmed) != 1 || p.Warmed[0] != "/project" {
		t.Fatalf("warm paths = %v", p.Warmed)
	}
	_ = s.Admin.SessionAdmin.Lifecycle.AttachAmbientOnSessionCreate(context.Background(), wire.CreateSessionRequest{
		Posture: wire.SessionPostureBuild,
	}, "sess-test")
	if p.BriefCalls != 0 {
		t.Fatalf("brief calls = %d", p.BriefCalls)
	}
}
