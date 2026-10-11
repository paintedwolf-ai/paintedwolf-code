package session

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/curationctx"
	"github.com/lycaon/lycaon/internal/hostresources"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/session/profiles"
	"github.com/lycaon/lycaon/internal/session/toolcontext"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
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

type occupancyTurnClient struct {
	modelcall.LLMClient
	contexts []context.Context
}

func (c *occupancyTurnClient) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	c.contexts = append(c.contexts, ctx)
	return c.LLMClient.Stream(ctx, req)
}

func TestAdmittedTurnCarriesUtilityOccupancyToProvider(t *testing.T) {
	mgr, st := newTestManager(t)
	client := &occupancyTurnClient{LLMClient: mgr.Coordinator.Model.LLM}
	mgr.Coordinator.Model.LLM = client
	sess, err := st.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create admitted session", err)
	_, err = mgr.Submissions.Prompt(t.Context(), sess.ID, "Continue the investigation.")
	testutil.FailErr(t, "execute admitted prompt", err)
	if len(client.contexts) == 0 {
		t.Fatal("admitted prompt never reached the mock provider")
	}
	for _, ctx := range client.contexts {
		identity := curationctx.SessionFrom(ctx)
		if !curationctx.LaneOccupied(ctx) || identity.SessionID != sess.ID || identity.ProjectID != sess.ProjectID {
			t.Fatalf("provider context lost admitted utility occupancy or identity: occupied=%v identity=%+v", curationctx.LaneOccupied(ctx), identity)
		}
	}
	if curationctx.LaneOccupied(t.Context()) || curationctx.SessionFrom(t.Context()).SessionID != "" {
		t.Fatal("turn occupancy escaped into the caller's context")
	}
}
