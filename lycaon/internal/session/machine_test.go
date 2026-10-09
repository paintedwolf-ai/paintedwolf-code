package session

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/hostresources"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestPromptSurfaceFingerprintTracksSkillAvailability(t *testing.T) {
	first := prompts.AgentPromptSurface{Skills: []prompts.AgentSkillView{{Name: "first", Description: "First procedure"}}}
	second := prompts.AgentPromptSurface{Skills: []prompts.AgentSkillView{{Name: "second", Description: "Second procedure"}}}
	if promptSurfaceFingerprint(first) != promptSurfaceFingerprint(second) {
		t.Fatal("skill catalog changes that do not alter prompt text should keep the prefix")
	}
	if promptSurfaceFingerprint(first) == promptSurfaceFingerprint(prompts.AgentPromptSurface{}) {
		t.Fatal("skill availability changes the prompt procedure")
	}
}

func TestAttachSkillReadRootsPrefersMachine(t *testing.T) {
	tctx := &tools.ToolContext{}
	attachSkillReadRoots(context.Background(), &Manager{}, nil, "", inject.Machine{
		ProfileID: "coordinator",
		ReadRoots: []string{"/skills/a", "/skills/b"},
	}, tctx)
	if len(tctx.Files.ReadRoots) != 2 || tctx.Files.ReadRoots[0] != "/skills/a" || tctx.Files.ReadRoots[1] != "/skills/b" {
		t.Fatalf("ReadRoots = %v", tctx.Files.ReadRoots)
	}
}

func TestCompileMachineZeroSession(t *testing.T) {
	var mgr *Manager
	if got := mgr.CompileMachine(context.Background(), nil, "coordinator"); got.Compiled() {
		t.Fatalf("nil compile = %+v", got)
	}
}

func TestPromptSurfaceWriteAgentSeesOmitAllowPresence(t *testing.T) {
	mgr := &Manager{}
	sess := &api.Session{ID: "s1", AgentType: "implementer"}
	snapshot := hostresources.Snapshot{Resources: []hostresources.State{{
		ID: "docker", Label: "Docker", Category: "Containers",
		Status: hostresources.StatusAvailable, HostSupport: hostresources.HostSupported,
		Access: hostresources.AccessAllow, Prompt: hostresources.PromptOmit,
		Surfaces: []hostresources.ExecutionSurface{hostresources.SurfaceProcessExec},
	}}}
	surfaces := []hostresources.ExecutionSurface{hostresources.SurfaceProcessExec}
	write := mgr.promptSurfaceFrom(context.Background(), sess, nil, snapshot, surfaces, true)
	if len(write.HostResources) != 1 || write.HostResources[0].ID != "docker" {
		t.Fatalf("write-agent surface = %#v", write.HostResources)
	}
	if write.HostResources[0].Category != "Containers" {
		t.Fatalf("category = %q", write.HostResources[0].Category)
	}
	read := mgr.promptSurfaceFrom(context.Background(), sess, nil, snapshot, surfaces, false)
	if len(read.HostResources) != 0 {
		t.Fatalf("read-only surface = %#v", read.HostResources)
	}
}
