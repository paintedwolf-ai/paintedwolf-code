package session

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/hostresources"
	"github.com/lycaon/lycaon/internal/session/profiles"
	"github.com/lycaon/lycaon/internal/session/toolcontext"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestAttachSkillReadRootsPrefersMachine(t *testing.T) {
	tctx := &tools.ToolContext{}
	(&toolcontext.Service{}).AttachSkillReadRoots(context.Background(), nil, "", inject.Machine{
		ProfileID: "coordinator",
		ReadRoots: []string{"/skills/a", "/skills/b"},
	}, tctx)
	if len(tctx.Files.ReadRoots) != 2 || tctx.Files.ReadRoots[0] != "/skills/a" || tctx.Files.ReadRoots[1] != "/skills/b" {
		t.Fatalf("ReadRoots = %v", tctx.Files.ReadRoots)
	}
}

func TestCompileMachineZeroSession(t *testing.T) {
	var mgr *profiles.Service
	if got := mgr.CompileMachine(context.Background(), nil, "coordinator"); got.Compiled() {
		t.Fatalf("nil compile = %+v", got)
	}
}

func TestPromptSurfaceWriteAgentSeesOmitAllowPresence(t *testing.T) {
	mgr := &profiles.Service{}
	sess := &api.Session{ID: "s1", AgentType: "implementer"}
	snapshot := hostresources.Snapshot{Resources: []hostresources.State{{
		ID: "docker", Label: "Docker", Category: "Containers",
		Status: hostresources.StatusAvailable, HostSupport: hostresources.HostSupported,
		Access: hostresources.AccessAllow, Prompt: hostresources.PromptOmit,
		Surfaces: []hostresources.ExecutionSurface{hostresources.SurfaceProcessExec},
	}}}
	surfaces := []hostresources.ExecutionSurface{hostresources.SurfaceProcessExec}
	write := mgr.PromptSurface(context.Background(), sess, nil, snapshot, surfaces, true)
	if len(write.HostResources) != 1 || write.HostResources[0].ID != "docker" {
		t.Fatalf("write-agent surface = %#v", write.HostResources)
	}
	if write.HostResources[0].Category != "Containers" {
		t.Fatalf("category = %q", write.HostResources[0].Category)
	}
	read := mgr.PromptSurface(context.Background(), sess, nil, snapshot, surfaces, false)
	if len(read.HostResources) != 0 {
		t.Fatalf("read-only surface = %#v", read.HostResources)
	}
}
