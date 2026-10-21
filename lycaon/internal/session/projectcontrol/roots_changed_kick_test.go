package projectcontrol

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/kick"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestEnqueueRootsChangedKickExtendsPendingNoticeBeforeNextTurn(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	ctx := t.Context()
	reg := project.NewMemoryRegistry()
	apiDir, webDir, docsDir := t.TempDir(), t.TempDir(), t.TempDir()
	created, err := reg.Create(ctx, project.CreateParams{
		Roots: []project.AttachRootParams{{Path: apiDir, Label: "api"}, {Path: webDir, Label: "web"}},
	})
	testutil.FailErr(t, "create project", err)

	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: configlayout.FindModuleRoot()})
	kicks := &kick.KickEngine{}
	kicks.SetPromptEngine(engine)
	anchorReg, err := anchor.LoadRegistryFromConfigRoot()
	testutil.FailErr(t, "load anchor registry", err)
	bus := anchor.NewBus(kicks)
	bus.SetRegistry(anchorReg)

	var history projectroot.History
	sessions := &detachSessions{sessions: []*api.Session{
		{ID: "sess-1", ProjectID: created.ID, WorkspaceRootID: created.Roots[0].ID, Status: api.SessionStatusIdle},
		{ID: "other", ProjectID: "other-project", Status: api.SessionStatusIdle},
	}}
	svc := New(sessions, nil, nil, nil, nil, nil, nil)
	svc.SetAnchors(bus)
	svc.SetRootHistory(&history)

	// No-op root set records and notifies nothing.
	initial := project.RootRefsFrom(created)
	svc.EnqueueRootsChangedKick(ctx, created.ID, initial, initial)
	if _, ok := kicks.PeekPendingKickID("sess-1"); ok {
		t.Fatal("unchanged roots queued a kick")
	}

	// Change 1 before the next turn: attach docs.
	attached, err := reg.AttachRoot(ctx, created.ID, project.AttachRootParams{Path: docsDir, Label: "docs"})
	testutil.FailErr(t, "attach docs", err)
	svc.EnqueueRootsChangedKick(ctx, created.ID, project.RootRefsFrom(attached.Before), project.RootRefsFrom(attached.After))

	// Change 2 before that turn runs: rename web -> site.
	siteLabel := "site"
	renamed, err := reg.PatchRoot(ctx, created.ID, attached.After.Roots[1].ID, project.PatchRootParams{Label: &siteLabel})
	testutil.FailErr(t, "rename web", err)
	svc.EnqueueRootsChangedKick(ctx, created.ID, project.RootRefsFrom(renamed.Before), project.RootRefsFrom(renamed.After))

	// Change 3 before that turn runs: detach docs. So only the rename remains net,
	// while add+rename before a turn extends the single pending notice.
	secondDocsDir := t.TempDir()
	attachedExtra, err := reg.AttachRoot(ctx, created.ID, project.AttachRootParams{Path: secondDocsDir, Label: "extra"})
	testutil.FailErr(t, "attach extra", err)
	svc.EnqueueRootsChangedKick(ctx, created.ID, project.RootRefsFrom(attachedExtra.Before), project.RootRefsFrom(attachedExtra.After))

	detachedDocs, err := reg.DetachRoot(ctx, created.ID, attached.Added.ID)
	testutil.FailErr(t, "detach docs", err)
	svc.EnqueueRootsChangedKick(ctx, created.ID, project.RootRefsFrom(detachedDocs.Before), project.RootRefsFrom(detachedDocs.After))

	// Exactly one notice is delivered at the turn boundary, naming both changes.
	kickID := kicks.TakePendingKickID("sess-1")
	if kickID != "coordinator-roots-changed" {
		t.Fatalf("pending kick = %q want coordinator-roots-changed", kickID)
	}
	text, lease, ok, err := kicks.RenderPendingNudge(ctx, "sess-1", kick.CoordinatorKickRenderContext{})
	testutil.FailErr(t, "render roots changed nudge", err)
	if !ok {
		t.Fatal("expected rendered roots changed nudge")
	}
	kicks.AckPendingNudge("sess-1", lease)
	if next := kicks.TakePendingKickID("sess-1"); next != "" {
		t.Fatalf("second roots changed kick queued: %q", next)
	}
	for _, want := range []string{"added extra → ", "renamed web → site", "• api", "• site", "• extra"} {
		if !strings.Contains(text, want) {
			t.Fatalf("extended notice missing %q:\n%s", want, text)
		}
	}
	if _, ok := kicks.PeekPendingKickID("other"); ok {
		t.Fatal("foreign session received roots changed kick")
	}

	// History still recorded the transient docs folder as retired.
	current := project.RootRefsFrom(detachedDocs.After)
	if r, stale := history.View(created.ID).StaleLabel(current, "docs"); !stale || r.Reason != projectroot.RootDetached {
		t.Fatalf("transient docs retirement = %+v,%v", r, stale)
	}
	if r, stale := history.View(created.ID).StaleLabel(current, "web"); !stale || r.Reason != projectroot.RootRenamed || r.Successor.Label != "site" {
		t.Fatalf("renamed web retirement = %+v,%v", r, stale)
	}

	// After the turn consumed the first notice, a new change starts a fresh notice.
	detachedExtra, err := reg.DetachRoot(ctx, created.ID, attachedExtra.Added.ID)
	testutil.FailErr(t, "detach extra", err)
	svc.EnqueueRootsChangedKick(ctx, created.ID, project.RootRefsFrom(detachedExtra.Before), project.RootRefsFrom(detachedExtra.After))
	if got := kicks.TakePendingKickID("sess-1"); got != "coordinator-roots-changed" {
		t.Fatalf("next turn kick = %q", got)
	}
	text2, lease2, ok, err := kicks.RenderPendingNudge(ctx, "sess-1", kick.CoordinatorKickRenderContext{})
	testutil.FailErr(t, "render second nudge", err)
	if !ok || !strings.Contains(text2, "removed extra") || strings.Contains(text2, "renamed web") {
		t.Fatalf("second turn notice = %q", text2)
	}
	kicks.AckPendingNudge("sess-1", lease2)

	// Attaching and detaching the same folder before a turn runs drops the pending notice.
	transientDir := t.TempDir()
	attachedTransient, err := reg.AttachRoot(ctx, created.ID, project.AttachRootParams{Path: transientDir, Label: "transient"})
	testutil.FailErr(t, "attach transient", err)
	svc.EnqueueRootsChangedKick(ctx, created.ID, project.RootRefsFrom(attachedTransient.Before), project.RootRefsFrom(attachedTransient.After))
	detachedTransient, err := reg.DetachRoot(ctx, created.ID, attachedTransient.Added.ID)
	testutil.FailErr(t, "detach transient", err)
	svc.EnqueueRootsChangedKick(ctx, created.ID, project.RootRefsFrom(detachedTransient.Before), project.RootRefsFrom(detachedTransient.After))
	if _, ok := kicks.PeekPendingKickID("sess-1"); ok {
		t.Fatal("net-empty change left a pending kick")
	}
}
