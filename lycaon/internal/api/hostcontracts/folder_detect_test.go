package hostcontracts

import (
	"testing"

	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectcontrib"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestFolderDetectPreviewsEnabledExtensionSuggestions(t *testing.T) {
	srv, reg := contractfixture.ExtensionsScopeServer(t)
	root := t.TempDir()
	contractfixture.WriteOverlay(t, root, extpacks.DeviceDesiredName, `format: 1
suggest:
  - id: acme/reviewed
    source: https://example.com/acme/reviewed.git
    version: ^1.0.0
`)

	p, err := project.CreateWithRoot(t.Context(), reg, root)
	testutil.FailErr(t, "create project", err)
	out := contractfixture.DetectFolder(t, srv, root)
	if out.ProjectID != p.ID {
		t.Fatalf("project_id=%q, want %q", out.ProjectID, p.ID)
	}
	if len(out.Projects) != 1 || out.Projects[0].ID != p.ID || !out.Projects[0].IsSingleRoot {
		t.Fatalf("projects=%+v, want single-root match for %q", out.Projects, p.ID)
	}
	if out.ExtensionSuggestions == nil {
		t.Fatal("extension suggestions missing")
	}
	if out.ExtensionSuggestions.SuggestionRevision == "" {
		t.Fatal("suggestion revision missing")
	}
	if len(out.ExtensionSuggestions.Suggestions) != 1 ||
		out.ExtensionSuggestions.Suggestions[0].ID != "acme/reviewed" ||
		out.ExtensionSuggestions.Suggestions[0].Status != wire.ExtensionSuggestionAvailable {
		t.Fatalf("suggestions=%+v", out.ExtensionSuggestions.Suggestions)
	}
}

func TestFolderDetectDistinguishesSingleAndMultiRootMatches(t *testing.T) {
	srv, reg := contractfixture.ExtensionsScopeServer(t)
	mainDir := t.TempDir()
	sharedDir := t.TempDir()
	contractfixture.WriteExtensionSuggestion(t, sharedDir)

	multi, err := reg.Create(t.Context(), project.CreateParams{
		Name: "Multi Workspace",
		Roots: []project.AttachRootParams{
			{Path: mainDir},
			{Path: sharedDir},
		},
	})
	testutil.FailErr(t, "create multi-root project", err)
	_, err = reg.SetTrustEnabled(t.Context(), multi.ID,
		map[string]bool{projectcontrib.SurfaceExtensionSuggestions: false})
	testutil.FailErr(t, "disable multi-root suggestions", err)

	// When only a multi-root project contains sharedDir, ProjectID is omitted so
	// callers do not silently treat the multi-root project as an exact single-root match,
	// and extension suggestions are evaluated for a fresh single-root project.
	onlyMulti := contractfixture.DetectFolder(t, srv, sharedDir)
	if onlyMulti.ProjectID != "" {
		t.Fatalf("project_id=%q, want empty when only multi-root matches exist", onlyMulti.ProjectID)
	}
	if len(onlyMulti.Projects) != 1 {
		t.Fatalf("len(projects)=%d, want 1", len(onlyMulti.Projects))
	}
	if onlyMulti.Projects[0].ID != multi.ID || onlyMulti.Projects[0].IsSingleRoot || len(onlyMulti.Projects[0].Roots) != 2 {
		t.Fatalf("projects[0]=%+v, want multi-root match with 2 roots", onlyMulti.Projects[0])
	}
	if onlyMulti.ExtensionSuggestions == nil {
		t.Fatal("expected extension suggestions to be evaluated against a fresh single-root subject")
	}

	// Adding a dedicated single-root project puts the single-root match first and populates project_id.
	single, err := project.CreateWithRoot(t.Context(), reg, sharedDir)
	testutil.FailErr(t, "create single-root project", err)
	testutil.FailErr(t, "touch multi-root", reg.TouchLastOpened(t.Context(), multi.ID))

	both := contractfixture.DetectFolder(t, srv, sharedDir)
	if both.ProjectID != single.ID {
		t.Fatalf("project_id=%q, want %q", both.ProjectID, single.ID)
	}
	if len(both.Projects) != 2 {
		t.Fatalf("len(projects)=%d, want 2", len(both.Projects))
	}
	if both.Projects[0].ID != single.ID || !both.Projects[0].IsSingleRoot {
		t.Fatalf("projects[0]=%+v, want single-root match first", both.Projects[0])
	}
	if both.Projects[1].ID != multi.ID || both.Projects[1].IsSingleRoot {
		t.Fatalf("projects[1]=%+v, want multi-root match second", both.Projects[1])
	}
}

func TestFolderDetectOmitsDisabledExtensionSuggestions(t *testing.T) {
	t.Run("device", func(t *testing.T) {
		srv, _ := contractfixture.ExtensionsScopeServer(t)
		root := t.TempDir()
		contractfixture.WriteExtensionSuggestion(t, root)
		testutil.FailErr(t, "disable device suggestions", srv.Admin.Project.Trust.Settings.TrustSurfaces.PutEnabled(
			map[string]bool{projectcontrib.SurfaceExtensionSuggestions: false}))

		if got := contractfixture.DetectFolder(t, srv, root).ExtensionSuggestions; got != nil {
			t.Fatalf("extension_suggestions=%+v, want omitted", got)
		}
	})

	t.Run("project", func(t *testing.T) {
		srv, reg := contractfixture.ExtensionsScopeServer(t)
		root := t.TempDir()
		contractfixture.WriteExtensionSuggestion(t, root)
		p, err := project.CreateWithRoot(t.Context(), reg, root)
		testutil.FailErr(t, "create project", err)
		_, err = reg.SetTrustEnabled(t.Context(), p.ID,
			map[string]bool{projectcontrib.SurfaceExtensionSuggestions: false})
		testutil.FailErr(t, "disable project suggestions", err)

		if got := contractfixture.DetectFolder(t, srv, root).ExtensionSuggestions; got != nil {
			t.Fatalf("extension_suggestions=%+v, want omitted", got)
		}
	})
}
