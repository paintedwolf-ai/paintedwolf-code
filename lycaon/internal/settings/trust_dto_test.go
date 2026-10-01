package settings

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectcontrib"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
)

func writeTrustFixture(t *testing.T, root, rel, body string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Dir(full), 0o755))
	testutil.FailErr(t, "write "+rel, os.WriteFile(full, []byte(body), 0o644))
}

func newTrustStore(t *testing.T) *TrustSurfacesStore {
	t.Helper()
	store, err := NewTrustSurfacesStoreAt(filepath.Join(t.TempDir(), "trust-surfaces.yaml"))
	testutil.FailErr(t, "NewTrustSurfacesStoreAt", err)
	return store
}

func projectAt(root string) project.Project {
	return project.Project{ID: "p", Roots: []project.Root{{ID: "root", Path: root}}}
}

func TestProjectTrustItemsIdentifyTheirRoots(t *testing.T) {
	t.Parallel()
	first := t.TempDir()
	second := t.TempDir()
	writeTrustFixture(t, first, "AGENTS.md", "first\n")
	writeTrustFixture(t, second, "AGENTS.md", "second\n")
	p := project.Project{ID: "p", Roots: []project.Root{
		{ID: "first", Path: first},
		{ID: "second", Path: second},
	}}

	got := ProjectTrustToDTO(newTrustStore(t), p, scanTrustManifest(t, project.RootPaths(&p)))
	found := map[string]bool{}
	for _, surface := range got.Surfaces {
		if surface.ID != projectcontrib.SurfaceAgentsMD {
			continue
		}
		for _, item := range surface.Items {
			found[item.RootID] = true
		}
	}
	if !found["first"] || !found["second"] || len(found) != 2 {
		t.Fatalf("instruction roots = %v, want first and second", found)
	}
}

func TestProjectTrustCountsChangedFiles(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	store := newTrustStore(t)

	empty := ProjectTrustToDTO(store, projectAt(root), scanTrustManifest(t, []string{root}))
	if len(empty.Review.Changes) != 0 || empty.UnreadCount != 0 {
		t.Fatalf("empty project changes: %+v", empty)
	}

	writeTrustFixture(t, root, "AGENTS.md", "guidance\n")
	writeTrustFixture(t, root, filepath.Join(settingsoverlay.DirName(), "approvals.yaml"), "posture: strict\n")
	got := ProjectTrustToDTO(store, projectAt(root), scanTrustManifest(t, []string{root}))
	if len(got.Review.Changes) != 2 || got.UnreadCount != 2 {
		t.Fatalf("configured project changes: %+v", got)
	}
}

func TestProjectTrustReviewIncludesDisabledSurfaces(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTrustFixture(t, root, "AGENTS.md", "guidance\n")
	writeTrustFixture(t, root, filepath.Join(settingsoverlay.DirName(), "approvals.yaml"), "posture: strict\n")

	p := projectAt(root)
	p.TrustEnabled = map[string]bool{projectcontrib.SurfaceProjectSettings: false}
	got := ProjectTrustToDTO(newTrustStore(t), p, scanTrustManifest(t, project.RootPaths(&p)))
	if len(got.Review.Changes) != 2 || got.UnreadCount != 2 {
		t.Fatalf("disabled surface missing from review: %+v", got)
	}
}

func TestProjectTrustReviewIncludesDeviceDisabledSurfaces(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	testutil.FailErr(t, "mark current scan configuration", settingsoverlay.EnsureCurrentFormat(root))
	writeTrustFixture(t, root, filepath.Join(settingsoverlay.DirName(), "ignores.yaml"), "version: 1\nfindings: []\n")
	store := newTrustStore(t)
	testutil.FailErr(t, "disable scan configuration", store.PutEnabled(map[string]bool{
		projectcontrib.SurfaceScanConfig: false,
	}))

	got := ProjectTrustToDTO(store, projectAt(root), scanTrustManifest(t, []string{root}))
	if len(got.Review.Changes) != 2 || got.UnreadCount != 2 {
		t.Fatalf("device-disabled surface missing from review: %+v", got)
	}
}

func TestProjectTrustReviewIncludesSuggestions(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTrustFixture(t, root, filepath.Join(settingsoverlay.DirName(), "extensions.yaml"),
		"format: 1\nsuggest:\n  - id: acme/triage\n")

	got := ProjectTrustToDTO(newTrustStore(t), projectAt(root), scanTrustManifest(t, []string{root}))
	if len(got.Review.Changes) != 1 || got.UnreadCount != 1 {
		t.Fatalf("suggestions missing from review: %+v", got)
	}
}

func TestProjectTrustUnseenSurfaceStillApplies(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTrustFixture(t, root, filepath.Join(settingsoverlay.DirName(), "approvals.yaml"), "posture: strict\n")

	got := ProjectTrustToDTO(newTrustStore(t), projectAt(root), scanTrustManifest(t, []string{root}))
	var settings *wireSurface
	for i := range got.Surfaces {
		if string(got.Surfaces[i].ID) == projectcontrib.SurfaceProjectSettings {
			settings = &wireSurface{Applying: got.Surfaces[i].Applying, Seen: got.Surfaces[i].Seen}
		}
	}
	if settings == nil {
		t.Fatal("project_settings surface missing from the response")
	}
	if !settings.Applying {
		t.Fatal("an unread surface was reported as not applying; reading is not a gate")
	}
	if settings.Seen {
		t.Fatal("a never-read surface reported as seen")
	}
}

type wireSurface struct {
	Applying bool
	Seen     bool
}

func TestSeenRecordsForCoversEverySurface(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTrustFixture(t, root, filepath.Join(settingsoverlay.DirName(), "approvals.yaml"), "posture: strict\n")

	records := SeenRecordsFor(project.Project{Roots: []project.Root{{ID: "root-1", Path: root}}}, scanTrustManifest(t, []string{root}))
	for _, row := range projectcontrib.Registry() {
		if _, ok := records[row.ID]; !ok {
			t.Fatalf("surface %s absent from seen records; its dot could never clear", row.ID)
		}
	}
}

func TestValidateTrustSwitchIdsRejectsUnknown(t *testing.T) {
	t.Parallel()
	if err := ValidateTrustSwitchIds(map[string]bool{"agents_md": false}); err != nil {
		t.Fatal("instruction surface was rejected")
	}
	if err := ValidateTrustSwitchIds(map[string]bool{"project_overlays": false}); err == nil {
		t.Fatal("unknown surface was accepted")
	}
}

func scanTrustManifest(t *testing.T, roots []string) projectcontrib.Manifest {
	t.Helper()
	inventory := projectcontrib.NewInventory()
	defer inventory.Close()
	manifest, err := inventory.Scan(t.Context(), roots)
	testutil.FailErr(t, "scan project trust", err)
	return manifest
}
