package projectcontrol

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/kick"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hostresources"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/session/profiles"
	sessionscope "github.com/lycaon/lycaon/internal/session/scope"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestEnqueuePostureChangedKickCoalescesAndDropsNoOp(t *testing.T) {
	ctx := t.Context()
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: configlayout.FindModuleRoot()})
	kicks := &kick.KickEngine{}
	kicks.SetPromptEngine(engine)
	anchorReg, err := anchor.LoadRegistryFromConfigRoot()
	testutil.FailErr(t, "load anchor registry", err)
	bus := anchor.NewBus(kicks)
	bus.SetRegistry(anchorReg)

	sessions := &detachSessions{sessions: []*api.Session{
		{ID: "sess-1", ProjectID: "proj-1", Status: api.SessionStatusIdle},
		{ID: "other", ProjectID: "proj-2", Status: api.SessionStatusIdle},
	}}
	svc := New(sessions, nil, nil, nil, nil, nil, nil)
	svc.SetAnchors(bus)

	// Moving between light and balanced does not alter outside-folder write review.
	svc.EnqueuePostureChangedKick(ctx, "proj-1", gate.PostureBalanced, gate.PostureLight)
	if _, ok := kicks.PeekPendingKickID("sess-1"); ok {
		t.Fatal("balanced -> light queued a posture kick")
	}
	svc.EnqueuePostureChangedKick(ctx, "proj-1", gate.PostureLight, gate.PostureBalanced)
	if _, ok := kicks.PeekPendingKickID("sess-1"); ok {
		t.Fatal("light -> balanced queued a posture kick")
	}

	// Crossing into strict queues one notice for the project's sessions only.
	svc.EnqueuePostureChangedKick(ctx, "proj-1", gate.PostureBalanced, gate.PostureStrict)
	if _, ok := kicks.PeekPendingKickID("other"); ok {
		t.Fatal("foreign project session received posture kick")
	}
	if got := kicks.TakePendingKickID("sess-1"); got != "coordinator-posture-changed" {
		t.Fatalf("pending kick = %q want coordinator-posture-changed", got)
	}
	text, lease, ok, err := kicks.RenderPendingNudge(ctx, "sess-1", kick.CoordinatorKickRenderContext{})
	testutil.FailErr(t, "render strict posture nudge", err)
	if !ok || strings.TrimSpace(text) != "The person now wants to approve writes outside the project, including temporary folders." {
		t.Fatalf("strict nudge = %q (ok=%v)", text, ok)
	}
	kicks.AckPendingNudge("sess-1", lease)

	// Repeated changes before the next turn coalesce; strict -> balanced -> light delivers one non-strict notice.
	svc.EnqueuePostureChangedKick(ctx, "proj-1", gate.PostureStrict, gate.PostureBalanced)
	svc.EnqueuePostureChangedKick(ctx, "proj-1", gate.PostureBalanced, gate.PostureLight)
	if got := kicks.TakePendingKickID("sess-1"); got != "coordinator-posture-changed" {
		t.Fatalf("coalesced pending kick = %q want coordinator-posture-changed", got)
	}
	text2, lease2, ok, err := kicks.RenderPendingNudge(ctx, "sess-1", kick.CoordinatorKickRenderContext{})
	testutil.FailErr(t, "render non-strict posture nudge", err)
	if !ok || strings.TrimSpace(text2) != "The person now allows writes to temporary and cache folders without asking; other paths outside the project still ask first." {
		t.Fatalf("non-strict nudge = %q (ok=%v)", text2, ok)
	}
	kicks.AckPendingNudge("sess-1", lease2)
	if extra := kicks.TakePendingKickID("sess-1"); extra != "" {
		t.Fatalf("duplicate posture kick queued: %q", extra)
	}

	// Reverting balanced -> strict -> balanced before the next turn drops the pending notice.
	svc.EnqueuePostureChangedKick(ctx, "proj-1", gate.PostureBalanced, gate.PostureStrict)
	svc.EnqueuePostureChangedKick(ctx, "proj-1", gate.PostureStrict, gate.PostureBalanced)
	if _, ok := kicks.PeekPendingKickID("sess-1"); ok {
		t.Fatal("reverted posture change left a pending kick")
	}
}

func TestRepositoryApprovalsOverlayTighteningChangesPromptAndEnqueuesNotice(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	ctx := t.Context()
	reg := project.NewMemoryRegistry()
	projectDir := t.TempDir()
	created, err := reg.Create(ctx, project.CreateParams{
		Roots: []project.AttachRootParams{{Path: projectDir, Label: "app"}},
	})
	testutil.FailErr(t, "create project", err)
	roots := project.RootRefsFrom(created)

	approvals, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "global-approvals.yaml"))
	testutil.FailErr(t, "new approval store", err)

	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: configlayout.FindModuleRoot()})
	kicks := &kick.KickEngine{}
	kicks.SetPromptEngine(engine)
	anchorReg, err := anchor.LoadRegistryFromConfigRoot()
	testutil.FailErr(t, "load anchor registry", err)
	bus := anchor.NewBus(kicks)
	bus.SetRegistry(anchorReg)

	sess := &api.Session{
		ID:              "sess-overlay",
		ProjectID:       created.ID,
		WorkspaceRootID: created.Roots[0].ID,
		WorkspacePath:   projectDir,
		Status:          api.SessionStatusIdle,
	}
	sessions := &detachSessions{sessions: []*api.Session{sess}}
	svc := New(sessions, nil, nil, nil, nil, nil, nil)
	svc.SetAnchors(bus)

	workspace := sessionscope.New(nil)
	workspace.SetProjects(reg)
	prof := profiles.New(sessions, nil, workspace)
	prof.SetProjects(reg)
	prof.SetApprovals(approvals)
	prof.SetPostureObserver(svc.ObserveSessionPosture)

	toolProfiles, err := sandbox.LoadToolProfiles()
	testutil.FailErr(t, "load tool profiles", err)
	renderPrompt := func(surface prompts.AgentPromptSurface) string {
		vars := prompts.AgentPromptSurfaceTemplateVars(surface)
		vars["tool_profile"] = "implement"
		prompts.MergeWorkspaceRootsVars(vars, roots, projectDir)
		if err := prompts.MergeAgentToolSurfaceVars("implement", nil, nil, prompts.SurfaceTurn{}, vars, toolProfiles); err != nil {
			t.Fatalf("MergeAgentToolSurfaceVars: %v", err)
		}
		out, err := engine.Render(ctx, "partials/command-surface.md", vars)
		testutil.FailErr(t, "render command-surface", err)
		return out
	}

	// Turn 1: device posture is balanced and no repository overlay exists.
	surface1 := prof.PromptSurface(ctx, sess, nil, hostresources.Snapshot{}, nil, true)
	if surface1.Posture != gate.PostureBalanced {
		t.Fatalf("turn 1 posture = %q want balanced", surface1.Posture)
	}
	prompt1 := renderPrompt(surface1)
	if !strings.Contains(prompt1, "Other paths ask the person first, except temporary and cache folders.") {
		t.Fatalf("turn 1 prompt missing balanced copy:\n%s", prompt1)
	}
	if _, ok := kicks.PeekPendingKickID(sess.ID); ok {
		t.Fatal("initial turn queued a posture change notice")
	}

	// Repository .paintedwolf/approvals.yaml tightens the project to strict.
	overlayPath := filepath.Join(projectDir, ".paintedwolf", "approvals.yaml")
	testutil.FailErr(t, "mkdir .paintedwolf", os.MkdirAll(filepath.Dir(overlayPath), 0o755))
	testutil.FailErr(t, "write repository approvals.yaml", os.WriteFile(overlayPath, []byte("approval_posture: strict\n"), 0o600))

	// Turn 2: effective posture reads strict from the repository overlay, updates prompt copy, and queues one notice.
	surface2 := prof.PromptSurface(ctx, sess, nil, hostresources.Snapshot{}, nil, true)
	if surface2.Posture != gate.PostureStrict {
		t.Fatalf("turn 2 posture = %q want strict", surface2.Posture)
	}
	prompt2 := renderPrompt(surface2)
	if !strings.Contains(prompt2, "Other paths, including temporary folders, ask the person first.") {
		t.Fatalf("turn 2 prompt missing strict copy:\n%s", prompt2)
	}
	if strings.Contains(prompt2, "except temporary and cache folders") {
		t.Fatalf("turn 2 prompt kept non-strict copy:\n%s", prompt2)
	}
	if got := kicks.TakePendingKickID(sess.ID); got != "coordinator-posture-changed" {
		t.Fatalf("pending kick after overlay tightening = %q want coordinator-posture-changed", got)
	}
	notice, lease, ok, err := kicks.RenderPendingNudge(ctx, sess.ID, kick.CoordinatorKickRenderContext{})
	testutil.FailErr(t, "render overlay tightening notice", err)
	if !ok || strings.TrimSpace(notice) != "The person now wants to approve writes outside the project, including temporary folders." {
		t.Fatalf("overlay tightening notice = %q (ok=%v)", notice, ok)
	}
	kicks.AckPendingNudge(sess.ID, lease)
}
